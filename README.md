# Snowflake-id service
The snowflake-id service generates globally unique, increasing 64 bits integer IDs. The service 
supports up to 1024 running snowflake-id servers. In the case of blue-green deployment, the max number of servers is 512. 
Theoretically, each server can generate up to 4,096,000 ids per second. Read [survey of distributed id](https://github.com/pengubco/notes/blob/main/survey-distributed-id.md) 
to learn more whether snowflake ids is a good solution for your use cases.

Servers are stateless and do not depend on persisted storage. Servers do not talk with each other. Instead, servers coordinate 
through [etcd](https://etcd.io/). 

## How to run the code?
The snowflake-id server needs read and write permission of a prefix on etcd. In order to run locally, follow the [Appendix 1](#1-Prepare-a-local-etcd-server) 
to setup a etcd server on the localhost. Also install a gRPC client that can get snowflake ID from the snowflake-id service. 
[Insomnia](https://insomnia.rest/) or [grpc-client-cli](https://github.com/vadimi/grpc-client-cli) would work.

### Run one snowflake-id server as a Go program
```bash
make build-go
# assume the config file is ./example/config/localhost.json
./build/snowflake-id -config_from_file=./example/config/localhost.json
# make gRPC call to get snowflake ID
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID localhost:7669
```

### Run one snowflake-id server as a Docker container
The latest build image is published on DockerHub [pengubco/snowflake-id](https://hub.docker.com/repository/docker/pengubco/snowflake-id/general). 
```bash
make build-docker
# assume the config file is ./example/config/docker.json
docker run --rm --name snowflake-id --mount type=bind,source="$(pwd)"/example/config,target=/app-config -p 7669:7669 snowflake-id -config_from_file=/app-config/docker.json
# make gRPC call to get snowflake ID
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID localhost:7669
```

### Run a cluster of 5 snowflake-id servers in Kubernetes.
The setup is for demo purpose. 
```bash
# Step 1. create a namespace for demo
kubectl create namespace snowflake-id
# Step 2. create a pod running etcd and expose it through NodePort Service.
kubectl create -f ./example/pod-etcd.yaml 
# Step 3. setup user, role in etcd. Execute commands in Appendix 1 inside the etcd pod. 
# Step 4. create a configmap to store the json config
kubectl create -f ./example/configmap.yaml 
# Step 5. deploy the snowflake-id service
kubectl create -f ./example/deployment.yaml 

# make gRPC call to get snowflake ID. 
echo '{}' | grpc-client-cli --proto ./api/v1/snowflake.proto --service=SnowflakeID --method=nextID [hostname-or-ip-of-node]:30001
```

## Appendix
### 1. Prepare a local etcd server. 
Step 1. [Install](https://etcd.io/docs/v3.5/install/) the etcd 3.5
Step 2. Setup user, role and permission. Learn more about etcd [RBAC](https://etcd.io/docs/v3.5/op-guide/authentication/rbac/).
```bash
etcdctl --user root:root user add snowflake-id --new-user-password="passwd"
etcdctl --user root:root role add snowflake-id
etcdctl --user root:root role grant-permission --prefix=true snowflake-id readwrite /snowflake-id/ 
etcdctl --user root:root user grant snowflake-id snowflake-id
# test read and write
etcdctl --user snowflake-id:passwd put /snowflake-id/k1 v1
etcdctl --user snowflake-id:passwd get /snowflake-id/k1 
etcdctl --user snowflake-id:passwd del /snowflake-id/k1 
```
