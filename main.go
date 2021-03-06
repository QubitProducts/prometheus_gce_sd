package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	log "github.com/golang/glog"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v2"

	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	"os/signal"
	"syscall"
)

var (
	configFilename    = flag.String("config", "", "Path to config file")
	outputFilename    = flag.String("output", "", "Path to results file")
	discoveryInterval = flag.Duration("discovery.interval", 30*time.Second, "Period of discovery update")
	discoveryTimeout  = flag.Duration("discovery.timeout", 25*time.Second, "Timeout of discovery update")
	metricsAddr       = flag.String("metrics.addr", ":8080", "Address to serve metrics on")

	targetCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gcesd_targets",
		Help: "Number of targets discovered, by job name",
	}, []string{"job"})
	syncDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "gcesd_sync_duration_seconds",
		Help: "Duration of the GCE api to prometheus target sync operation",
	})
	syncResult = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gcesd_sync_count",
		Help: "Count of the GCE api to prometheus target sync operation, labeled by result",
	}, []string{"result"})
	resultWrite = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gcesd_target_write_count",
		Help: "Number of times that the output file is updated",
	})
)

func init() {
	prometheus.MustRegister(targetCount)
	prometheus.MustRegister(syncDuration)
	prometheus.MustRegister(syncResult)
	prometheus.MustRegister(resultWrite)
}

type SearchConfig struct {
	Job     string   `yaml:"job"`
	Tags    []string `yaml:"tags"`
	Project string   `yaml:"project"`
	Ports   []int    `yaml:"ports"`

	XXX map[string]interface{} `yaml:",inline"`
}

type DiscoveryTarget struct {
	Targets []string          `yaml:"targets"`
	Labels  map[string]string `yaml:"labels"`
}

func NewComputeService(ctx context.Context) (*compute.Service, error) {
	client, err := google.DefaultClient(ctx, compute.ComputeScope)
	if err != nil {
		return nil, errors.Wrapf(err, "Unable to get client")
	}

	service, err := compute.New(client)
	if err != nil {
		return nil, errors.Wrap(err, "Unable to create compute service")
	}

	return service, nil
}

func LoadConfigFile(path string) ([]SearchConfig, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return []SearchConfig{}, errors.Wrap(err, "Unable to read config file")
	}

	var config []SearchConfig
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return []SearchConfig{}, errors.Wrap(err, "Unable to parse config file")
	}

	for i, c := range config {
		err := ValidateConfig(c)
		if err != nil {
			return []SearchConfig{}, errors.Wrapf(err, "Failed to validate config entry #%v", i)
		}
	}

	return config, nil
}

func ValidateConfig(conf SearchConfig) error {
	if len(conf.XXX) != 0 {
		unknownKeys := []string{}
		for k := range conf.XXX {
			unknownKeys = append(unknownKeys, k)
		}

		return errors.Errorf("Unknown keys in config: %v", strings.Join(unknownKeys, ","))
	}

	if conf.Job == "" {
		return errors.New("No job specified")
	}

	if len(conf.Tags) == 0 {
		return errors.New("No tags specified")
	}

	if conf.Project == "" {
		return errors.New("No project specified")
	}

	if len(conf.Ports) == 0 {
		return errors.New("No ports specified")
	}

	return nil
}

func DiscoverTargets(ctx context.Context, searchConfigs []SearchConfig) ([]DiscoveryTarget, error) {
	targets := []DiscoveryTarget{}

	instancesByProject := map[string][]*compute.Instance{}

	for _, config := range searchConfigs {
		allInstances, ok := instancesByProject[config.Project]
		if !ok {
			var err error
			allInstances, err = listAllInstances(ctx, config.Project)
			if err != nil {
				return []DiscoveryTarget{}, errors.Wrapf(err, "Failed to list instances in %v", config.Project)
			}
			instancesByProject[config.Project] = allInstances
		}

		instances, err := DiscoverComputeByTags(ctx, allInstances, config.Tags)
		if err != nil {
			return []DiscoveryTarget{}, errors.Wrapf(err, "Failed to discover instances %v in %v", config.Tags, config.Project)
		}
		log.V(2).Infof("Found %v targets for %v in %v", len(instances), config.Tags, config.Project)

		for _, instance := range instances {
			instTargets, err := InstanceToTargets(instance, config)
			if err != nil {
				return []DiscoveryTarget{}, errors.Wrapf(err, "Failed to convert %v to a discovery target", instance)
			}
			targets = append(targets, instTargets...)
		}
	}

	counts := map[string]int{}
	for _, t := range targets {
		job := t.Labels["job"]
		counts[job] = counts[job] + 1
	}
	for j, c := range counts {
		targetCount.WithLabelValues(j).Set(float64(c))
	}

	return targets, nil
}

func InstanceToTargets(instance *compute.Instance, config SearchConfig) ([]DiscoveryTarget, error) {
	ip, err := findInstanceIP(instance)
	if err != nil {
		return []DiscoveryTarget{}, errors.Wrap(err, "Could not find ip for instance")
	}

	targets := []DiscoveryTarget{}
	for _, port := range config.Ports {
		targets = append(targets, DiscoveryTarget{
			Targets: []string{fmt.Sprintf("%v:%v", ip, port)},
			Labels: map[string]string{
				"job": config.Job,
				"__meta_gce_instance_tags":    fmt.Sprintf(",%v,", strings.Join(instance.Tags.Items, ",")),
				"__meta_gce_instance_zone":    parseResource(instance.Zone),
				"__meta_gce_instance_type":    parseResource(instance.MachineType),
				"__meta_gce_instance_project": config.Project,
				"__meta_gce_instance_name":    instance.Name,
			},
		})
	}
	return targets, nil
}

func DiscoverComputeByTags(ctx context.Context, allInstances []*compute.Instance, searchTags []string) ([]*compute.Instance, error) {
	instances := []*compute.Instance{}
	for _, instance := range allInstances {
		if instance == nil {
			continue
		}

		if tagsMatch(searchTags, instance.Tags.Items) {
			instances = append(instances, instance)
		}
	}

	return instances, nil
}

func listAllInstances(ctx context.Context, project string) ([]*compute.Instance, error) {
	service, err := NewComputeService(ctx)
	if err != nil {
		return []*compute.Instance{}, err
	}

	instances := []*compute.Instance{}
	err = service.Instances.AggregatedList(project).Pages(ctx, func(ilist *compute.InstanceAggregatedList) error {
		for _, innerIList := range ilist.Items {
			for _, instance := range innerIList.Instances {
				if instance == nil {
					log.Infof("Skipping nil instance in %v", project)
					continue
				}

				instances = append(instances, instance)
			}
		}
		return nil
	})

	return instances, errors.Wrap(err, "Failed to list instances")
}

func tagsMatch(searchTags, instanceTags []string) bool {
	for _, st := range searchTags {
		found := false
		for _, it := range instanceTags {
			if st == it {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func parseResource(resource string) string {
	parts := strings.Split(resource, "/")
	return parts[len(parts)-1]
}

func formatTag(tag string) string {
	return strings.ToLower(strings.Replace(tag, "-", "_", -1))
}

func findInstanceIP(instance *compute.Instance) (string, error) {
	for _, iface := range instance.NetworkInterfaces {
		if iface == nil {
			continue
		}

		return iface.NetworkIP, nil
	}
	return "", errors.Errorf("No non nil interfaces found")
}

func WriteTargets(ctx context.Context, targets []DiscoveryTarget, targetFile string) error {
	sortedTargets := discoveryTargets(targets)
	sort.Sort(sortedTargets)
	targets = []DiscoveryTarget(sortedTargets)

	d, err := yaml.Marshal(targets)
	if err != nil {
		return errors.Wrap(err, "Failed to marshal targets")
	}

	f, err := os.Create(targetFile)
	if err != nil {
		return errors.Wrap(err, "Failed to open output file")
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	_, err = w.WriteString(string(d))
	if err != nil {
		return errors.Wrap(err, "Failed to write to output buffer")
	}
	err = w.Flush()
	if err != nil {
		return errors.Wrap(err, "Failed to flush to output file")
	}
	return nil
}

func targetsDifferent(old, new []DiscoveryTarget) bool {
	oldSorted := discoveryTargets(old)
	sort.Sort(oldSorted)
	old = []DiscoveryTarget(oldSorted)
	newSorted := discoveryTargets(new)
	sort.Sort(newSorted)
	new = []DiscoveryTarget(newSorted)

	newEncoded, _ := yaml.Marshal(new)
	oldEncoded, _ := yaml.Marshal(old)

	return !bytes.Equal(oldEncoded, newEncoded)
}

type discoveryTargets []DiscoveryTarget

func (dt discoveryTargets) Len() int           { return len(dt) }
func (dt discoveryTargets) Less(i, j int) bool { return dt[i].Targets[0] < dt[j].Targets[0] }
func (dt discoveryTargets) Swap(i, j int)      { dt[i], dt[j] = dt[j], dt[i] }

func tickAndListen(ctx context.Context, interval time.Duration) chan bool {
	tChan := make(chan bool, 2)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGUSR1)

	go func() {
		for ctx.Err() == nil {
			select {
			case <-time.After(interval):
				tChan <- false
			case <-sigChan:
				tChan <- true
			case <-ctx.Done():
			}
		}
	}()
	// Let's kick things off with a bang!
	tChan <- false

	return tChan
}

func main() {
	flag.Parse()
	ctx := context.Background()

	if *configFilename == "" {
		log.Error("Config filename not specified")
		os.Exit(1)
	}
	if *outputFilename == "" {
		log.Error("Output filename not specified")
		os.Exit(1)
	}

	config, err := LoadConfigFile(*configFilename)
	if err != nil {
		log.Errorf("Failed to load config file %v: %v", *configFilename, err)
		os.Exit(1)
	}
	log.V(2).Infof("Loaded config: %v", config)

	go func() {
		http.Handle("/metrics", prometheus.Handler())
		err := http.ListenAndServe(*metricsAddr, nil)
		if err != nil {
			log.Errorf("Could not start metrics server on %v: %v", *metricsAddr, err)
			os.Exit(1)
		}
	}()

	var currentTargets []DiscoveryTarget

	loop := func(force bool) error {
		ctx, cancel := context.WithTimeout(ctx, *discoveryTimeout)
		defer cancel()

		started := time.Now()
		defer syncDuration.Observe(float64(started.Sub(time.Now())) / float64(time.Second))

		log.V(2).Info("Discovering targets")
		newTargets, err := DiscoverTargets(ctx, config)
		if err != nil {
			return errors.Wrap(err, "Could not discover targets")
		}

		if force {
			log.Info("Forcing write")
		} else if !targetsDifferent(newTargets, currentTargets) {
			log.V(2).Info("No changes detected, skipping write")
			return nil
		}

		log.V(2).Info("Writing targets")
		resultWrite.Inc()
		err = WriteTargets(ctx, newTargets, *outputFilename)
		if err != nil {
			return errors.Wrap(err, "Could not write targets")
		}
		currentTargets = newTargets
		return nil
	}

	for force := range tickAndListen(ctx, *discoveryInterval) {
		err := loop(force)
		if err != nil {
			log.Errorf("Sync loop failed: %v", err)
			syncResult.WithLabelValues("failure").Inc()
		} else {
			syncResult.WithLabelValues("success").Inc()
		}
	}
}
