package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
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
	Tag     string `yaml:"tag"`
	Project string `yaml:"project"`
	Ports   []int  `yaml:"ports"`
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

	return config, nil
}

func DiscoverComputeByProjectTag(ctx context.Context, project, tag string) ([]string, error) {
	service, err := NewComputeService(ctx)
	if err != nil {
		return []string{}, err
	}

	// Honestly, you can apparantly do .Filter("tags eq dataflow").Do() here, but i cant get it to work.
	ilist, err := service.Instances.AggregatedList(project).Context(ctx).Do()
	if err != nil {
		return []string{}, errors.Wrap(err, "Failed to list instances")
	}

	ips := []string{}
	for _, innerIList := range ilist.Items {
		for _, instance := range innerIList.Instances {
			if instance == nil {
				log.Infof("Skipping nil instance in %v", project)
				continue
			}

			ip, err := findInstanceIP(*instance)
			if err != nil {
				log.Errorf("Could not find IP for instance: %+v", err)
				continue
			}

			log.V(2).Infof("Instance %v/%v", project, ip)
			for _, tagn := range instance.Tags.Items {
				if tagn != tag {
					continue
				}

				ips = append(ips, ip)
			}
		}
	}

	log.V(2).Infof("Found %v targets for %v in %v", len(ips), tag, project)
	return ips, nil
}

func findInstanceIP(instance compute.Instance) (string, error) {
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

func DiscoverTargets(ctx context.Context, searchConfigs []SearchConfig) ([]DiscoveryTarget, error) {
	targets := make([]DiscoveryTarget, 0, len(searchConfigs))

	for _, config := range searchConfigs {
		ips, err := DiscoverComputeByProjectTag(ctx, config.Project, config.Tag)
		if err != nil {
			return []DiscoveryTarget{}, errors.Wrapf(err, "Failed to discover instances %v in %v", config.Tag, config.Project)
		}

		endpoints := make([]string, 0, len(ips)*len(config.Ports))
		for _, ip := range ips {
			for _, port := range config.Ports {
				endpoints = append(endpoints, fmt.Sprintf("%v:%v", ip, port))
			}
		}

		targets = append(targets, DiscoveryTarget{
			Targets: endpoints,
			Labels: map[string]string{
				"tag": config.Tag,
			},
		})
	}

	return targets, nil
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
