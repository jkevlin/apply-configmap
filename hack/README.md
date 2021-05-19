# Demo

## In a terminal window

```bash
minikube start

kubectl apply -f shell-demo.yaml
kubectl apply -f test-deploy.yaml
kubectl apply -f create-configmap.yaml
# wait for pod to be ready
kubectl get pod 

# If you want to build a new version
env GOOS=linux GOARCH=arm64 go build -o apply-configmap ../client/cmd/kubeclient
# Copy artifacts to minikube
kubectl apply -f https://raw.githubusercontent.com/stakater/Reloader/master/deployments/kubernetes/reloader.yaml
kubectl cp apply-configmap /shell-demo:/
kubectl cp update-configmap-even.yaml /shell-demo:/
kubectl cp update-configmap-odd.yaml /shell-demo:/
kubectl exec -it shell-demo -- /bin/bash
```

## In container terminal

```bash
# create the configmap
./apply-configmap -namespace='default' -file='update-configmap-even.yaml'
# update the configmap
./apply-configmap -namespace='default' -file='update-configmap-odd.yaml'
exit
```

## Back in the terminal window

```bash
kubectl get configmap some-configmap -o yaml
```

