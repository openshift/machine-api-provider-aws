# Machine API Provider AWS

This repository contains implementations of AWS Provider for [Machine API](https://github.com/openshift/machine-api-operator).

## What is the Machine API

A declarative API for creating and managing machines in an OpenShift cluster. The project is based on v1alpha2 version of [Cluster API](https://github.com/kubernetes-sigs/cluster-api).

## Documentation

- [Overview](https://github.com/openshift/machine-api-operator/blob/master/docs/user/machine-api-operator-overview.md)
- [Hacking Guide](https://github.com/openshift/machine-api-operator/blob/master/docs/dev/hacking-guide.md)

## Architecture

The provider imports [Machine controller](https://github.com/openshift/machine-api-operator/tree/master/pkg/controller/machine) from `machine-api-operator` and provides implementation for Actuator interface. The Actuator implementation is responsible for CRUD operations on AWS API.

## Building and running controller locally

```
NO_DOCKER=1 make build && ./bin/machine-controller-manager
```

By default, we run make tasks in a container. To run the controller locally, set NO_DOCKER=1.

## Running tests

### Unit

In order to run unit tests use `make test`.

### E2E Tests

If you wish to run E2E tests, you can use `make e2e`. Make sure you have a  running OpenShift cluster on AWS.
