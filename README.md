# prometheus_gce_sd

This tool allows Prometheus to automatically discover instances running on Google Compute Engine

## Building

``` go
make bootstrap build
```

We use [Glide](https://github.com/Masterminds/glide) for dep management

## Running

prometheus_gce_sd requires two arguments, a config file and an output file. The output file should be in the directory read by prometheus.

``` go
$ cat >./config.yaml <EOF
- tag: zookeeper
  project: my-gcp-project
  ports:
    - 8080
EOF
$ prometheus_gce_sd -config ./config.yaml -output ./output.yaml &
$ cat output.yaml
- targets:
  - 10.0.0.3:8080
  - 10.0.0.4:8080
  - 10.0.0.5:8080
  - 10.0.0.6:8080
  labels:
    tag: zookeeper
```
