package main

import (
	"testing"

	"encoding/json"
	"fmt"
	"reflect"
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
					Tag:     "Zookeeper",
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

func prettyPrint(i interface{}) string {
	v, err := json.Marshal(i)
	if err != nil {
		return fmt.Sprintf("%v", i)
	}
	return string(v)
}
