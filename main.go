package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	"gopkg.in/yaml.v2"

	compute "google.golang.org/api/compute/v1"
)

var (
	scope = compute.ComputeScope

	tag     = flag.String("tag", os.Getenv("COMPUTE_TAG"), "What tag should we discover?")
	project = flag.String("project", os.Getenv("GCE_PROJECT"), "What project should we watch?")
	outfile = flag.String("filetowrite", os.Getenv("FILE_TO_WRITE"), "Where should we write our results? eg, /tmp/out.yml")
	port    = flag.String("porttowatch", os.Getenv("PORT_TO_WATCH"), "What port should we poll? eg. 80")
	route   = flag.String("route", os.Getenv("ROUTE"), "What route should we get? /metrics")
)

type DiscoveryFile struct {
	Entries []DiscoveryTarget `yaml:""`
}

type DiscoveryTarget struct {
	Targets []string `yaml:"targets"`
	//MetricsPath string            `yaml:"metrics_path"`
	Labels map[string]string `yaml:"labels"`
}

func getNewComputeService() *compute.Service {
	client, err := google.DefaultClient(context.Background(), scope)
	if err != nil {
		glog.Fatal("Unable to get client")
	}

	service, err := compute.New(client)
	if err != nil {
		glog.Fatal("Unable to create compute service")
	}

	return service
}

func DiscoverComputeByTag(project, tag string) map[string]string {
	service := getNewComputeService()
	//fmt.Println(fmt.Sprintf("tag eq %v", tag))
	// Honestly, you can apparantly do .Filter("tags eq dataflow").Do() here, but i cant get it to work.
	ilist, err := service.Instances.AggregatedList(project).Do()
	if err != nil {
		glog.Fatal(err)
	}

	retval := make(map[string]string)
	for _, zoneval := range ilist.Items {
		for i, instance := range zoneval.Instances {
			for _, tagn := range zoneval.Instances[i].Tags.Items {
				if tagn == tag {
					retval[instance.Name] = instance.NetworkInterfaces[0].NetworkIP
				}
			}
		}
	}
	return retval
}

func main() {
	flag.Parse()
	fmt.Println("Running with args: ", *tag, *project, *outfile, *port, *route)
	for {
		f, err := os.Create(*outfile)
		if err != nil {
			glog.Fatal(err)
		}

		w := bufio.NewWriter(f)
		var targets = &DiscoveryTarget{}
		targets.Targets = []string{}
		targets.Labels = map[string]string{}
		//targets.MetricsPath = *route
		for _, address := range DiscoverComputeByTag(*project, *tag) {
			targets.Targets = append(targets.Targets, address+":"+*port)
		}
		targets.Labels["name"] = *tag

		var fs = &DiscoveryFile{
			Entries: []DiscoveryTarget{
				*targets,
			},
		}

		d, err := yaml.Marshal(&fs.Entries)
		w.WriteString(string(d))
		w.Flush()
		f.Close()
		time.Sleep(time.Second * 30)
	}
}
