#!/bin/bash

for n in {1..1000000}; 
do
    bin/etcdctl put foo_$n bar_$n
    echo foo_$n bar_$n
    sleep .01
done
