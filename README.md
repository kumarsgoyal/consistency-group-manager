# consistency-group-manager


## log pod running instruction


### to generate proto files

`protoc --go_out=. --go-grpc_out=. proto/counter.proto `

### to build image

`podman build -t cg-manager:latest .`

### create new image using same tag

`podman tag cg-manager docker.io/yourusername/cg-manager:latest`

`podman push docker.io/yourusername/cg-manager:latest`

### to apply statefulset and service

`kubectl apply -f sts.yaml`

## to view data

`kubectl apply -f debug-pod.yaml `

`kubectl exec -it debug-pod-log -- sh`

`kubectl exec -it debug-pod-log -- sh -c 'cat /app/data/replica-*/*'`


## other useful commands


### to run local registry

`podman run -d -p 5000:5000 --name local-registry docker.io/library/registry:2`

### to start minikube

`minikube start -p local --insecure-registry="localhost:5000" --driver=podman --container-runtime=containerd`

### to edit registry config

`sudo vi /etc/containers/registries.conf`

### to load images in minikube (to get image defined in yaml file)

`minikube -p local image load localhost:5000/cg-manager`

### to login in minikube

`minikube ssh`

`sudo passwd root`
