#!/usr/bin/env python3
"""Counts the user-space instructions of one compile in the compile benchmark
(crates/peeper/benches/compile.rs), all threads included. Unlike time, the
count barely moves with machine load, so two versions compare to a fraction
of a percent. The benchmark runs once with one compile and once with two; the
difference is one compile, without the setup around it. Linux only, like
`perf stat`, whose way of counting it follows.

    scripts/bench-instructions.py
"""

import ctypes
import json
import os
import platform
import struct
import subprocess

PERF_EVENT_OPEN = {"x86_64": 298, "aarch64": 241}[platform.machine()]
HARDWARE, INSTRUCTIONS = 0, 1
DISABLED, INHERIT, EXCLUDE_KERNEL, EXCLUDE_HV, ENABLE_ON_EXEC = 1, 2, 1 << 5, 1 << 6, 1 << 12
libc = ctypes.CDLL(None, use_errno=True)


def benchmark():
    """Builds the benchmark and gives its executable's path."""
    command = ["cargo", "bench", "-p", "peeper", "--bench", "compile", "--no-run",
               "--message-format=json", "--offline", "--locked"]
    output = subprocess.run(command, capture_output=True, text=True, check=True).stdout
    for line in output.splitlines():
        message = json.loads(line)
        if message.get("reason") == "compiler-artifact" and message.get("executable") \
                and message["target"]["name"] == "compile":
            return message["executable"]
    raise SystemExit("cargo built no compile benchmark")


def instructions(executable, runs):
    """The instructions `executable` runs with PEEPER_BENCH_RUNS=`runs`."""
    start_reading, start = os.pipe()
    child = os.fork()
    if child == 0:  # waits until the counter is attached, so it counts from exec
        os.close(start)
        os.read(start_reading, 1)
        os.execve(executable, [executable],  # without clang: its processes would be counted too
                  {**os.environ, "PEEPER_BENCH_RUNS": str(runs), "PEEPER_BENCH_CLANG": "0"})
    os.close(start_reading)
    flags = DISABLED | INHERIT | EXCLUDE_KERNEL | EXCLUDE_HV | ENABLE_ON_EXEC
    attribute = struct.pack("IIQQQQQ", HARDWARE, 128, INSTRUCTIONS, 0, 0, 0, flags).ljust(128, b"\0")
    counter = libc.syscall(PERF_EVENT_OPEN, attribute, child, -1, -1, 0)
    if counter < 0:
        os.kill(child, 9)
        error = ctypes.get_errno()
        raise SystemExit(f"cannot count instructions: {os.strerror(error)} "
                         "(see /proc/sys/kernel/perf_event_paranoid)")
    os.write(start, b"go")
    os.close(start)
    _, status = os.waitpid(child, 0)
    if status != 0:
        raise SystemExit(f"the benchmark failed with status {status}")
    count = struct.unpack("Q", os.read(counter, 8))[0]
    os.close(counter)
    return count


executable = benchmark()
one, two = instructions(executable, 1), instructions(executable, 2)
print(f"one compile: {(two - one) / 1e6:,.1f} M instructions")
