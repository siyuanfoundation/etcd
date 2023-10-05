#!/bin/bash
for i in {1..1000000}
do 
    bin/etcdctl put "foo_$i" "foo_bar_jibberlish_$i"
    # echo "finished at foo_$i"
done
echo "done"