package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
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
	discoveryInterval = flag.Duration("discovery-interval", 30*time.Second, "Period of discovery update")
	discoveryTimeout  = flag.Duration("discovery-timeout", 25*time.Second, "Timeout of discovery update")
)

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
			target, err := InstanceToTarget(instance, config)
			if err != nil {
				return []DiscoveryTarget{}, errors.Wrapf(err, "Failed to convert %v to a discovery target", instance)
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
	tagLabels := ","
	for _, tag := range instance.Tags.Items {
		tagLabels = tagLabels + formatTag(tag) + ","
	}

	labels["job"] = config.Job
	labels["__meta_gce_instance_tags"] = tagLabels
	labels["__meta_gce_instance_zone"] = parseResource(instance.Zone)
	labels["__meta_gce_instance_type"] = parseResource(instance.MachineType)
	labels["__meta_gce_instance_project"] = config.Project

	return DiscoveryTarget{
		Targets: endpoints,
		Labels:  labels,
	}, nil
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
