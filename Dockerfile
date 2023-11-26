ARG ARCH=amd64
FROM --platform=linux/${ARCH} gcr.io/distroless/static-debian12

ADD bin/etcd /usr/local/bin/
ADD bin/etcdctl /usr/local/bin/
ADD bin/etcdutl /usr/local/bin/

WORKDIR /var/etcd/
WORKDIR /var/lib/etcd/

EXPOSE 2379 2380 6060

# Define default command.
CMD ["/usr/local/bin/etcd", "--experimental-enable-pprof", "--quota-backend-bytes=10737418240", "--experimental-backend-type=sqlite"]
