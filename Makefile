full-release:
	go get
	go build
	docker build -t qubit/gce-discoverer .
	docker tag -f qubit/gce-discoverer gcr.io/qubit-registry/gce-discoverer
	gcloud docker push gcr.io/qubit-registry/gce-discoverer

build:
	go build .

docker_build:
	docker run --rm -v "$$PWD":/go/src/github.com/qubitdigital/gce-discoverer \
	  -e GOPATH=/go \
	  -w /go/src/github.com/qubitdigital/gce-discoverer \
	  golang:1.7 make build
