# Contributing

## Build

```sh
npm install --prefix ui
npm run build --prefix ui
go build -o simota ./cmd/simota
```

## Run

```sh
./simota serve -config simota.yaml
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
