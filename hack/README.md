# Demo

## In a terminal window

```bash
minikube start --driver=docker

kubectl apply -f shell-demo.yaml
# wait for pod to be ready
kubectl get pod shell-demo

# If you want to build a new version
env GOOS=linux GOARCH=arm64 go build -o apply-configmap ../client/cmd/kubeclient
# Copy artifacts to minikube
kubectl cp apply-configmap /shell-demo:/
kubectl cp create-configmap.yaml /shell-demo:/
kubectl cp update-configmap.yaml /shell-demo:/
kubectl exec -it shell-demo -- /bin/bash
```

## In container terminal

```bash
# create the configmap
./apply-configmap -namespace='default' -file='create-configmap.yaml'
# update the configmap
./apply-configmap -namespace='default' -file='update-configmap.yaml'
exit
```

## Back in the terminal window

```bash
kubectl get configmap some-configmap -o yaml
```

