#!/bin/bash

for n in {1..10000}; 
do
    bin/etcdctl put foo_$n bar_$n
    echo foo_$n bar_$n
    sleep 1
done