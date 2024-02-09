#!/usr/bin/env bash
# usage: ./scripts/eetcd_benchmark.sh [--backend-type=sqlite] [--n=1] [--total-keys=10000] [--docker]
set -m

backendType=bolt
n=1
inDocker=false
totalKeys=100000
# Parse the command line arguments
while [ $# -gt 0 ]; do
  case "$1" in
    --backend-type=*)
      backendType="${1#*=}"
      shift
      ;;
    --n=*)
      n="${1#*=}"
      shift
      ;;
    --total-keys=*)
      totalKeys="${1#*=}"
      shift
      ;;
    --docker)
      inDocker=true
      shift
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

if $inDocker ; then
  echo "run benchmark for backendType = ${backendType} ${n} times inside docker"
  etcdBin=etcd
  etcdctlBin=etcdctl
  benchmarkBin=benchmark
else
  echo "run benchmark for backendType = ${backendType} ${n} times locally"
  etcdBin=bin/etcd
  etcdctlBin=bin/etcdctl
  benchmarkBin=bin/tools/benchmark
fi

outputDir="out/e_etcd_benchmark_output"
if [ -d $outputDir ]; then
  echo "removing ${outputDir}"
  rm -rf $outputDir
fi
mkdir -p $outputDir
echo "saving output in ${outputDir}/"

function check_health {
  maxTimeToRestart=3600
  j=0
  while true; do
    if ((i >= maxTimeToRestart)); then
      echo "failed to verify etcd health in ${maxTimeToRestart} seconds"
      exit 1
    fi
    if $etcdctlBin endpoint health; then
      break
    fi
    ((j++))
    echo "have waited ${j}s for etcd to become healthy"
    sleep 1
  done
}

if pgrep etcd; then pkill etcd; fi
$etcdBin --enable-pprof --experimental-backend-type=$backendType --data-dir=$backendType.etcd --quota-backend-bytes=16106127360 > "${outputDir}/etcd.log" 2>&1 &
serverPID=$!
echo "starting etcd, serverPID = ${serverPID}"
check_health

for ((i = 1; i <= n; i++ )); do
  echo "running benchmark iteration: ${i}"
  sleep 5
  # collect cpu profile while writing into the db
  curl http://127.0.0.1:2379/debug/pprof/profile?seconds=30 > "${outputDir}/${backendType}_cpu_iter_${i}.pprof" &
  $benchmarkBin put --total=${totalKeys} --key-size=128 --val-size=1024 --key-space-size=100000000 > "${outputDir}/${backendType}_put_benchmark_results_iter_${i}.log"
  # collect memory profile
  sleep 60
  curl http://127.0.0.1:2379/debug/pprof/heap > "${outputDir}/${backendType}_mem_iter_${i}.pprof"
  # restart etcd and collect restore time and mem profile
  kill $serverPID
  wait
  sleep 5
  ls -lh ${backendType}.etcd/member/snap/ > "${outputDir}/${backendType}_db_size_iter_${i}.log"
  $etcdBin --enable-pprof --experimental-backend-type=$backendType --data-dir=$backendType.etcd --quota-backend-bytes=16106127360 > "${outputDir}/etcd_restart_iter_${i}.log" 2>&1 &
  serverPID=$!
  echo "restarting etcd(${i}), serverPID = ${serverPID}"
  start=$(date +%s)
  check_health
  end=$(date +%s)
  echo "Time to restore etcd: $(($end-$start)) seconds" | tee "${outputDir}/time_to_restore_iter_${i}"

  curl http://127.0.0.1:2379/debug/pprof/heap > "${outputDir}/${backendType}_restart_mem_iter_${i}.pprof"
done

echo "Finished benchmarking, check results in in ${outputDir}"
jobs
fg %$((2*$n+1))
