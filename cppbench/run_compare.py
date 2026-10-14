#!/usr/bin/env python3
"""Run serial, pinned, fixed-count Go/C++ comparisons and retain raw evidence."""
import argparse
import json
import os
from pathlib import Path
import statistics
import subprocess
import re

parser = argparse.ArgumentParser()
parser.add_argument('--cpu', type=int, default=6)
parser.add_argument('--samples', type=int, default=3)
parser.add_argument('--output', type=Path, required=True)
parser.add_argument('--binaries', type=Path, default=Path('/tmp'))
args = parser.parse_args()
root = Path(__file__).resolve().parents[2]
out = args.output.resolve()
out.mkdir(parents=True, exist_ok=True)
args.binaries.mkdir(parents=True, exist_ok=True)
go = args.binaries / 'turtle-go-compare-fresh'
cpp = args.binaries / 'turtle-compare-fresh'
env = dict(os.environ, GOMAXPROCS='1')
with (out/'build.log').open('w') as log:
    subprocess.run(['go', '-C', str(root/'go'), 'build', '-o', str(go), './cmd/cppcompare'], check=True, env=env, stdout=log, stderr=subprocess.STDOUT)
    subprocess.run(['python3', str(root/'go/cppbench/build.py'), '--output', str(cpp), '--fix-scanner-resume'], check=True, stdout=log, stderr=subprocess.STDOUT)
meta = dict(cpu=args.cpu, samples=args.samples, count=262144, scan_queries=4096,
            trace_bits=20, value_bytes=8, go='M=16384 L=65536 F=16; locking enabled',
            cpp='leaf=2097152 node=4096 batch=16384 memory_device=1073741824; null worker pool',
            scanner='temporary resume patch via build.py --fix-scanner-resume', commands=[])
for name, cmd in [('go', ['go','version']), ('compiler',['c++','--version']),('cpu',['lscpu'])]:
    result=subprocess.run(cmd, capture_output=True, text=True, check=True)
    (out/(name+'-environment.txt')).write_text(result.stdout+result.stderr)
summary={}
for n in [65536,1048576]:
    samples={}
    for sample in range(1,args.samples+1):
        jobs=[('go',[str(go),'-n',str(n),'-count','262144','-scans','4096','-trace-bits','20']),
              ('cpp-memtree',[str(cpp),str(n),'262144','2097152','16384','0','0','1','20','1073741824','8']),
              ('cpp-scans',[str(cpp),str(n),'262144','2097152','16384','4096','1','0','20','1073741824','8'])]
        texts={}
        for name,cmd in jobs:
            cmd=['taskset','-c',str(args.cpu)]+cmd
            print('Running',name,n,sample,flush=True)
            meta['commands'].append(cmd)
            (out/'metadata.json').write_text(json.dumps(meta,indent=2)+'\n')
            path=out/f'{name}-{n}-{sample}.txt'
            with path.open('w') as log:
                subprocess.run(cmd,stdout=log,stderr=subprocess.STDOUT,env=env,timeout=600,check=True)
            texts[name]=path.read_text()
            for op,length,value in re.findall(r'(?:Go|C\+\+)/([A-Za-z0-9]+)(?: N=\d+)?(?: length=(\d+))? ns/(?:op|key|scan)=([\d.eE+-]+)',texts[name]):
                key=name+'/'+op+('/'+length if length else '')
                samples.setdefault(key,[]).append(float(value))
        fingerprints=[re.search(r'Trace checksum=(\w+)',t)[1] for t in texts.values()]
        assert len(set(fingerprints))==1, fingerprints
        for op in ['TreeHit','MemTreeMixed50']:
            checks=[re.search(r'/'+op+r' .*checksum=(\d+)',texts[name])[1] for name in ['go','cpp-memtree']]
            assert len(set(checks))==1, checks
        for length in [1,100,1000]:
            checks=[re.search(r'length='+str(length)+r' .*visited=(\d+) checksum=(\d+)',texts[name]).groups() for name in ['go','cpp-scans']]
            assert len(set(checks))==1, checks
        print('Passed timing and cross-language checks',n,sample,flush=True)
    summary[str(n)]={key:dict(samples=values,median=statistics.median(values)) for key,values in samples.items()}
    (out/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
