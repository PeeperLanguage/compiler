# Incremental LSP Benchmark Report

Incremental LSP branch now compiles workspace requests through one shared compiler pipeline with parser-time fingerprints, phase-gated reuse, dependency-ready scheduling, and component-scoped root isolation. Benchmarks show happy-path edits keep parse work flat at one module while reusing or downgrading rest of affected component as designed.

Bench command:

```bash
env GOCACHE=/tmp/peeper-gocache go test ./internal/lsp -run '^$' -bench BenchmarkIncrementalWorkspace -benchmem
```

| Case | modules_parsed/op | modules_reused/op | wall time |
| --- | ---: | ---: | ---: |
| small/cold_compile | 6 | 0 | 3.20 ms/op |
| small/warm_no_change_open | 1 | 5 | 1.73 ms/op |
| small/body_only_edit | 1 | 4 | 1.81 ms/op |
| small/export_shape_edit | 1 | 4 | 1.93 ms/op |
| small/import_set_edit | 3 | 4 | 2.53 ms/op |
| small/unrelated_component_edit | 2 | 5 | 2.13 ms/op |
| small/multi_main_first_root | 6 | 0 | 3.34 ms/op |
| small/multi_main_second_root | 2 | 0 | 2.13 ms/op |
| medium/cold_compile | 14 | 0 | 8.93 ms/op |
| medium/warm_no_change_open | 1 | 13 | 3.37 ms/op |
| medium/body_only_edit | 1 | 12 | 3.49 ms/op |
| medium/export_shape_edit | 1 | 12 | 3.56 ms/op |
| medium/import_set_edit | 3 | 12 | 3.95 ms/op |
| medium/unrelated_component_edit | 2 | 13 | 4.09 ms/op |
| medium/multi_main_first_root | 14 | 0 | 7.86 ms/op |
| medium/multi_main_second_root | 2 | 0 | 3.82 ms/op |
| large/cold_compile | 26 | 0 | 14.92 ms/op |
| large/warm_no_change_open | 1 | 25 | 6.84 ms/op |
| large/body_only_edit | 1 | 24 | 7.51 ms/op |
| large/export_shape_edit | 1 | 24 | 7.36 ms/op |
| large/import_set_edit | 3 | 24 | 7.68 ms/op |
| large/unrelated_component_edit | 2 | 25 | 7.35 ms/op |
| large/multi_main_first_root | 26 | 0 | 15.42 ms/op |
| large/multi_main_second_root | 2 | 0 | 6.55 ms/op |

Multi-main isolation is visible in large fixture: second independent root compiles in 6.55 ms/op with 2 parsed modules, while first root pulls full 26-module chain at 15.42 ms/op.

Step 7 persistence is deferred: warm no-change and body-only edit already hold parse count at 1 across workspace sizes, so restart persistence is not justified until real data shows cold restart is dominant user bottleneck.
