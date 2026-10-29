# Contributing

## Build

### From source

```sh
npm install --prefix ui
npm run build --prefix ui
go build -o simota ./cmd/simota
```

### Container Image

```sh
rockcraft pack
```

## Run

```sh
./simota -config simota.yaml
```

## Test

```sh
go test ./...
golangci-lint run ./...
npm test --prefix ui
npm run lint --prefix ui
npm run typecheck --prefix ui
npm run format:check --prefix ui
```

The Kubernetes smoke test deploys `k8s/` on a kind cluster and checks Ella SIM OTA through its Service. It needs Docker, kind, kubectl, kubeconform, curl, jq, and the `simota:latest` image:

```sh
rockcraft.skopeo --insecure-policy copy oci-archive:simota_0.0.1_amd64.rock docker-daemon:simota:latest
ci/k8s/smoke.sh
```
