package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"time"

	log "github.com/golang/glog"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v2"

	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
)

var (
	configFilename    = flag.String("config", os.Getenv("GCE_DISCOVERER_CONFIG"), "Path to config file")
	outputFilename    = flag.String("output", os.Getenv("GCE_DISCOVERER_OUTPUT"), "Path to results file")
	discoveryInterval = flag.Duration("discovery-interval", 10*time.Second, "Period of discovery update")
	discoveryTimeout  = flag.Duration("discovery-timeout", 25*time.Second, "Timeout of discovery update")
)

type SearchConfig struct {
	Tags    []string `yaml:"tags"`
	Project string   `yaml:"project"`
	Ports   []int    `yaml:"ports"`
}

type Targets []string
type DiscoveryTarget struct {
	Targets Targets           `yaml:"targets"`
	Labels  map[string]string `yaml:"labels"`
}

// Implement the sort interface
func (t Targets) Len() int {
	return len(t)
}

func (t Targets) Less(i, j int) bool {
	return t[i] < t[j]
}

func (t Targets) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

func (dt DiscoveryTarget) IsEqual(ndt DiscoveryTarget) bool {
	sort.Sort(dt.Targets)
	sort.Sort(ndt.Targets)

	// Targets
	for i, t := range dt.Targets {
		if t != ndt.Targets[i] {
			return false
		}
	}

	// Labels
	for k, v := range dt.Labels {
		if _, ok := ndt.Labels[k]; !ok {
			// Doesnt contain the key
			return false
		}
		if ndt.Labels[k] != v {
			// Values dont match
			return false
		}
	}

	return true
}

type DiscoveryTargets []DiscoveryTarget

// Implement the sort interface
func (dt DiscoveryTargets) Len() int {
	return len(dt)
}

func (dt DiscoveryTargets) Less(i, j int) bool {
	return dt[i].Targets[0] < dt[j].Targets[0]
}

func (dt DiscoveryTargets) Swap(i, j int) {
	dt[i], dt[j] = dt[j], dt[i]
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

	return config, nil
}

func DiscoverTargets(ctx context.Context, searchConfigs []SearchConfig) (DiscoveryTargets, error) {
	targets := DiscoveryTargets{}

	for _, config := range searchConfigs {
		instances, err := DiscoverComputeByProjectTags(ctx, config.Project, config.Tags)
		if err != nil {
			return DiscoveryTargets{}, errors.Wrapf(err, "Failed to discover instances %v in %v", config.Tags, config.Project)
		}

		for _, instance := range instances {
			target, err := InstanceToTarget(instance, config)
			if err != nil {
				return DiscoveryTargets{}, err
			}
			targets = append(targets, target)
		}
	}

	return targets, nil
}

func InstanceToTarget(instance *compute.Instance, config SearchConfig) (DiscoveryTarget, error) {
	ip, err := findInstanceIP(instance)
	if err != nil {
		return DiscoveryTarget{}, errors.Wrap(err, "Could not find ip for instance")
	}

	endpoints := make([]string, 0, len(config.Ports))
	for _, port := range config.Ports {
		endpoints = append(endpoints, fmt.Sprintf("%v:%v", ip, port))
	}

	labels := map[string]string{}
	for _, tag := range instance.Tags.Items {
		labels[fmt.Sprintf("gce_instance_tag_%v", formatTag(tag))] = "true"
	}
	labels["gce_instance_zone"] = parseResource(instance.Zone)
	labels["gce_instance_type"] = parseResource(instance.MachineType)
	labels["gce_instance_project"] = config.Project

	return DiscoveryTarget{
		Targets: endpoints,
		Labels:  labels,
	}, nil
}

func DiscoverComputeByProjectTags(ctx context.Context, project string, searchTags []string) ([]*compute.Instance, error) {
	service, err := NewComputeService(ctx)
	if err != nil {
		return []*compute.Instance{}, err
	}

	// Honestly, you can apparantly do .Filter("tags eq dataflow").Do() here, but i cant get it to work.
	ilist, err := service.Instances.AggregatedList(project).Context(ctx).Do()
	if err != nil {
		return []*compute.Instance{}, errors.Wrap(err, "Failed to list instances")
	}

	instances := []*compute.Instance{}
	for _, innerIList := range ilist.Items {
		for _, instance := range innerIList.Instances {
			if instance == nil {
				log.Infof("Skipping nil instance in %v", project)
				continue
			}

			if tagsMatch(searchTags, instance.Tags.Items) {
				instances = append(instances, instance)
			}
		}
	}

	log.V(2).Infof("Found %v targets for %v in %v", len(instances), searchTags, project)
	return instances, nil
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

var (
	previousTargets DiscoveryTargets
)

func TargetsEqual(a, b DiscoveryTargets) bool {
	if len(a) != len(b) {
		return false
	}
	for i, _ := range a {
		if a[i].IsEqual(b[i]) {
			return false
		}
	}
	return true
}

func WriteTargets(ctx context.Context, targets DiscoveryTargets, targetFile string) error {
	sort.Sort(targets)
	// Lets check if we have any new targets, and only create a new file if we do.
	if !TargetsEqual(targets, previousTargets) {
		log.V(2).Info("Skipping write targets due to no new targets.")
		return nil
	}

	f, err := os.Create(targetFile)
	if err != nil {
		return errors.Wrap(err, "Failed to open output file")
	}
	defer f.Close()

	d, err := yaml.Marshal(targets)
	if err != nil {
		return errors.Wrap(err, "Failed to marshal targets")
	}

	w := bufio.NewWriter(f)
	_, err = w.WriteString(string(d))
	if err != nil {
		return errors.Wrap(err, "Failed to write to output buffer")
	}
	err = w.Flush()
	if err != nil {
		return errors.Wrap(err, "Failed to flush to output file")
	}

	previousTargets = targets
	return nil
}

func main() {
	flag.Parse()
	ctx := context.Background()

	if *configFilename == "" {
		log.Fatalf("Config filename not specified")
	}
	if *outputFilename == "" {
		log.Fatalf("Output filename not specified")
	}

	config, err := LoadConfigFile(*configFilename)
	if err != nil {
		log.Fatalf("Config loading failed: %+v", err)
	}

	log.V(2).Infof("Loaded config: %v", config)
	for range time.Tick(time.Second * 30) {
		ctx, cancel := context.WithTimeout(ctx, *discoveryTimeout)

		log.V(2).Info("Discovering targets")
		targets, err := DiscoverTargets(ctx, config)
		if err != nil {
			log.Errorf("Failed to run discovery: %+v", err)
			cancel()
			continue
		}

		log.V(2).Info("Writing targets")
		err = WriteTargets(ctx, targets, *outputFilename)
		if err != nil {
			log.Errorf("Failed to write output file: %+v", err)
		}

		cancel()
	}
}
