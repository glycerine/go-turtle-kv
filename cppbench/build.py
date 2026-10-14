#!/usr/bin/env python3
"""Reuse flags and libraries from the existing C++ Release build, without rebuilding it."""
import argparse
import json
from pathlib import Path
import shlex
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--build', type=Path, default=Path(__file__).resolve().parents[2] / 'build/Release')
parser.add_argument('--output', type=Path, default=Path('/tmp/turtle-cppbench'))
parser.add_argument('--fix-scanner-resume', action='store_true', help='link a temporary scanner object that resumes after an empty heap')
args = parser.parse_args()
root = Path(__file__).resolve().parents[2]
build = args.build.resolve()
commands = json.loads((build / 'compile_commands.json').read_text())
entry = next(c for c in commands if c['file'].endswith('in_memory_node.test.cpp'))
command = shlex.split(entry['command'])
oldroot = str(Path(entry['file']).parents[3])
# The build directory was moved with this repository; fix only project include
# paths. Dependency paths remain exactly as recorded by Conan.
command = [arg.replace(oldroot, str(root)) for arg in command]
command = command[:command.index('-o')]
obj = args.output.with_suffix('.o')
args.output.parent.mkdir(parents=True, exist_ok=True)
subprocess.run(command + ['-c', str(Path(__file__).with_name('main.cpp')), '-o', str(obj)], check=True)
extra_objects = []
if args.fix_scanner_resume:
    source = (root / 'src/turtle_kv/kv_store_scanner.cpp').read_text()
    before = """    if (this->heap_.empty()) {
      if (this->next_item_ && this->next_item_->value.is_delete()) {"""
    after = """    if (this->heap_.empty()) {
      // A drained leaf can leave no heap entries while later leaves remain.
      // Resume before treating an empty heap as end of the entire tree.
      if (this->needs_resume_) {
        BATT_REQUIRE_OK(this->resume());
        if (!this->heap_.empty()) {
          continue;
        }
      }
      if (this->next_item_ && this->next_item_->value.is_delete()) {"""
    if source.count(before) != 1:
        raise RuntimeError('scanner source changed; review the resume patch')
    patched = args.output.with_suffix('.scanner.cpp')
    patched.write_text(source.replace(before, after))
    scanner_obj = args.output.with_suffix('.scanner.o')
    subprocess.run(command + ['-c', str(patched), '-o', str(scanner_obj)], check=True)
    extra_objects.append(str(scanner_obj))
ninja = (build / 'build.ninja').read_text().splitlines()
target = next(i for i, line in enumerate(ninja) if line.startswith('build src/turtle_kv_Test:'))
libs = next(line.split(' = ', 1)[1] for line in ninja[target:] if line.startswith('  LINK_LIBRARIES = '))
subprocess.run([command[0], '-O3', '-g', '-fno-omit-frame-pointer', str(obj), '-o', str(args.output)] + extra_objects + shlex.split(libs), cwd=build, check=True)
print(args.output)
