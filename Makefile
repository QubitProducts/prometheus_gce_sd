
build:
	go get
	go build
	docker build -t qubit/gce-discoverer .
	docker tag -f qubit/gce-discoverer gcr.io/qubit-registry/gce-discoverer
	gcloud docker push gcr.io/qubit-registry/gce-discoverer

