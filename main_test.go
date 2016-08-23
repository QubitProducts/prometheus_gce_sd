package main

import (
	"testing"

	"encoding/json"
	"fmt"
	"reflect"

	compute "google.golang.org/api/compute/v1"
)

func TestLoadConfigFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path          string
		expected      []SearchConfig
		expectedError bool
	}{
		{
			path: "./test/config_valid.yaml",
			expected: []SearchConfig{
				{
					Tags:    []string{"Zookeeper"},
					Project: "sandbox",
					Ports:   []int{8080, 6060},
				},
			},
			expectedError: false,
		},
		{
			path:          "./test/config_malformed.yaml",
			expectedError: true,
		},
		{
			path:          "./test/config_missing.yaml",
			expectedError: true,
		},
	}

	for _, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			t.Parallel()

			res, err := LoadConfigFile(c.path)
			if c.expectedError {
				if err == nil {
					t.Fatalf("Unexpected success\nResult: %v", prettyPrint(res))
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error\nError: %v", err)
				}

				if reflect.DeepEqual(res, c.expected) {
					t.Fatalf("Discrepancy in result\nResult: %v", prettyPrint(res))
				}
			}
		})
	}
}

func TestInstanceToTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		instance      *compute.Instance
		config        SearchConfig
		expected      DiscoveryTarget
		expectedError bool
	}{
		{
			instance: &compute.Instance{
				Zone:        "https://www.googleapis.com/compute/v1/projects/qubit-vcloud-us-proc-stg/zones/us-central1-b",
				MachineType: "https://www.googleapis.com/compute/v1/projects/qubit-vcloud-us-proc-stg/zones/us-central1-b/machineTypes/g1-small",
				Tags: &compute.Tags{
					Items: []string{"foo"},
				},
				NetworkInterfaces: []*compute.NetworkInterface{
					{NetworkIP: "127.0.0.1"},
				},
			},
			config: SearchConfig{
				Ports:   []int{8080, 9090},
				Project: "test-project",
			},
			expected: DiscoveryTarget{
				Targets: []string{"127.0.0.1:8080", "127.0.0.1:9090"},
				Labels: map[string]string{
					"gce_instance_tag_foo": "true",
					"gce_instance_zone":    "us-central-1b",
					"gce_instance_type":    "g1-small",
					"gce_instance_project": "us-central-1b",
				},
			},
			expectedError: false,
		},
		{ // No network interfaces
			instance: &compute.Instance{
				Zone:        "https://www.googleapis.com/compute/v1/projects/qubit-vcloud-us-proc-stg/zones/us-central1-b",
				MachineType: "https://www.googleapis.com/compute/v1/projects/qubit-vcloud-us-proc-stg/zones/us-central1-b/machineTypes/g1-small",
				Tags: &compute.Tags{
					Items: []string{"foo"},
				},
				NetworkInterfaces: []*compute.NetworkInterface{},
			},
			config: SearchConfig{
				Ports:   []int{8080, 9090},
				Project: "test-project",
			},
			expectedError: true,
		},
		{ // Tags to lower and - to _
			instance: &compute.Instance{
				Zone:        "https://www.googleapis.com/compute/v1/projects/qubit-vcloud-us-proc-stg/zones/us-central1-b",
				MachineType: "https://www.googleapis.com/compute/v1/projects/qubit-vcloud-us-proc-stg/zones/us-central1-b/machineTypes/g1-small",
				Tags: &compute.Tags{
					Items: []string{"FOO-BAR"},
				},
				NetworkInterfaces: []*compute.NetworkInterface{
					{NetworkIP: "127.0.0.1"},
				},
			},
			config: SearchConfig{
				Ports:   []int{8080, 9090},
				Project: "test-project",
			},
			expected: DiscoveryTarget{
				Targets: []string{"127.0.0.1:8080", "127.0.0.1:9090"},
				Labels: map[string]string{
					"gce_instance_tag_foo_bar": "true",
					"gce_instance_zone":        "us-central-1b",
					"gce_instance_type":        "g1-small",
					"gce_instance_project":     "us-central-1b",
				},
			},
			expectedError: false,
		},
	}

	for _, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			t.Parallel()

			res, err := InstanceToTarget(c.instance, c.config)
			if c.expectedError {
				if err == nil {
					t.Fatalf("Unexpected success\nResult: %v", prettyPrint(res))
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error\nError: %v", err)
				}

				if reflect.DeepEqual(res, c.expected) {
					t.Fatalf("Discrepancy in result\nResult: %v", prettyPrint(res))
				}
			}
		})
	}
}

func prettyPrint(i interface{}) string {
	v, err := json.Marshal(i)
	if err != nil {
		return fmt.Sprintf("%v", i)
	}
	return string(v)
}
