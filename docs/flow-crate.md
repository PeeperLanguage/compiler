# The `flow` crate

> The crates have moved since this was written: the phases are now
> `crates/syntax/*` (the tree crate is `tree`), `crates/check`,
> `crates/model`, `crates/lower`, `crates/flow` and `crates/eval`.

The path checks: errors that depend on which routes execution can take
through one function. Source: `crates/flow/src/`: `lib.rs` (the
shared engine), `missing_return.rs`, `unreachable.rs`, `variable_state.rs`,
and `loans/` (`mod.rs`, `places.rs`, `holders.rs`, `two_phase.rs`,
`passes.rs`, `errors.rs`). Each check only reads one function's
`mir::Body` and pushes `Diagnostic`s.

## 1. Who calls it, what it gives

The checker runs the four checks on each function's MIR
(`crates/check/src/functions/bodies.rs:84-90`).

```mermaid
flowchart LR
    subgraph check["check crate: functions/bodies.rs"]
        lower["lower::lower → mir::Body"]
    end
    lower --> VS["variable_state::report_variable_state_errors<br/>(body, file, diagnostics)<br/>T0039 T0034 T0035 T0007"]
    lower --> LO["loans::report_loan_errors<br/>(body, pointer_bytes, file, diagnostics)<br/>T0037 T0036 T0016"]
    lower --> UR["unreachable::warn_unreachable_code<br/>(body, file, diagnostics)<br/>W0001"]
    lower --> MR["missing_return::report_missing_return<br/>(body, file, result_text, diagnostics)<br/>T0017, last: points at the closing brace"]

    subgraph shared["lib.rs: shared by the checks"]
        DF["dataflow&lt;B, F, E&gt;<br/>(blocks, seeds, edges, run, join) → Vec&lt;Option&lt;F&gt;&gt;"]
        PR["predecessors(body) → Vec&lt;Vec&lt;Id&lt;Block&gt;&gt;&gt;"]
        BI["Bits&lt;T&gt;"]
    end
    VS --> DF & PR & BI
    LO --> DF & PR & BI
    MR --> PR
    VS & LO & UR & MR --> D[("Vec&lt;Diagnostic&gt;")]
```

## 2. `lib.rs`, `missing_return.rs`, `unreachable.rs`: the engine and the two small checks

```mermaid
classDiagram
    class Bits~T~ {
        words: Vec~u64~
        kind: PhantomData~fn() -> T~
        new(count) Bits~T~
        set(id)
        clear(id)
        toggle(id)
        has(id) bool
        ids() Iterator~Id~T~~
        combine(other, op: fn(u64, u64) -> u64) Bits~T~
    }
    note for Bits~T~ "One bit per Id~T~. Clone and PartialEq are written\nby hand, so T needs neither (T is an empty enum)."
```

```mermaid
flowchart TD
    MR["report_missing_return<br/>signature.result is none? → stop"] --> F1["find the block ending in<br/>Terminator::FallOff(span)"]
    F1 --> R["body.reachable() → Vec&lt;bool&gt;<br/>(on mir::Body, shared with LLVM)"]
    F1 -->|end block reachable| MC["missing_return_causes(body, end, reachable)<br/>→ Vec&lt;(Span, &str)&gt;"]
    MC --> PR["predecessors(body)"]
    MC -->|walks back through empty blocks;<br/>labels each Branch side| OUT["one T0017 error,<br/>at most SHOWN_CAUSES = 3 labels"]

    UR["warn_unreachable_code"] --> R
    UR --> W["one W0001 per unreached run of code"]
```

`dataflow` is the engine behind both big checks. It takes:

- `seeds`: the first blocks and their facts;
- `edges`: the blocks that come next;
- `run`: how one block changes the facts;
- `join`: how facts merge where paths meet.

It repeats until nothing changes, so loops work too. Forward, the edges are
`Terminator::targets`; backward, they are `predecessors`.

## 3. `variable_state.rs`: unset, moved, assigned twice

```mermaid
classDiagram
    class Tracked {
        <<empty enum>>
        only named as Id~Tracked~
    }
    class Facts {
        always: Bits~Tracked~
        maybe: Bits~Tracked~
        moved: Bits~Tracked~
        new(count) Facts
        declare(followed, set: bool)
        step(body, kind: &InstructionKind, tracked)
        join(other) Facts
    }
    Facts --> Bits : 3 sets
    Facts ..> AccessKind : step matches Write / Move / End
    Facts ..> PlaceRef : access.place.whole_slot()
    note for Facts "always: set on every path (joined with and)\nmaybe: set on some path (or)\nmoved: moved on some path, not set since (or)"
```

```mermaid
flowchart TD
    REP["report_variable_state_errors(body, file, diagnostics)"] --> TS["tracked_slots(body)<br/>→ (Vec&lt;Option&lt;Id&lt;Tracked&gt;&gt;&gt;, count)<br/>only slots with a `let` without value, or a Move"]
    TS -->|count == 0| STOP[not walked]
    TS --> DF["dataflow forward from the entry<br/>seed: Facts::new<br/>run: Facts::step per line<br/>join: Facts::join"]
    DF --> WALK["walk each block again from its start Facts;<br/>each line: kind.accesses(...)"]
    WALK -->|move of a part of a place| PM["partial_move(file, span, place: PlaceRef, lent_again)<br/>uses part_and_holder(place) → [&str; 2]"]
    WALK -->|whole write, maybe already set, no mut| SA["second_assignment(body, file, slot, span, set)<br/>T0007"]
    WALK -->|use, maybe moved| MS["move_sites(body, slot, block, position) → Vec&lt;Span&gt;<br/>searched back only when there is an error"]
    MS --> MU["moved_use(...)<br/>T0034"]
    WALK -->|use, maybe unset| UU["unset_use(...)<br/>T0039"]
    WALK --> STEP["Facts::step, then the next line"]
```

## 4. `loans/`: the types

A loan is one `Borrow` (then one parameter loan per parameter). A holder is a
pointer that may hold loans: each slot's holders, then one per instruction
giving a pointer, then one for a result kept in memory.

Which file holds what. `Holders` and `Loans` are defined in `mod.rs`; each
file adds the methods of its job in its own `impl` block.

| File | Holds |
| --- | --- |
| `mod.rs` | `Holder`, `Loan`, `Holders`, `Loans`, `report_loan_errors`, `holder_ids`, the walk `Loans::report` |
| `places.rs` | `Bytes`, `Holders::levels`, `Holders::first_level`, `known_until_through`, `names_exactly`, the `Bytes` test |
| `holders.rs` | numbering (`holder_count` ... `lent`), `uses`, `defines`, `Held`, `Lent`, `Borrowed` |
| `two_phase.rs` | `Reservation`, `Holders::reserved_loans` |
| `passes.rs` | `sorted_union`, `lines_that_matter`, `step_held`, `step_write`, `step_live`, `Loans::live_at_ends`, `Loans::held_at_starts` |
| `errors.rs` | `Access`, `accesses`, `broken_loan`, `move_of_read`, `activation_error`, `return_errors`, `label_later_use`, `next_use`, `holder_words`, `slot_name` |

```mermaid
classDiagram
    class Holder {
        <<empty enum>>
        a pointer that may hold loans
    }
    class Loan {
        <<empty enum>>
        one Borrow, then one per parameter
    }
    class Holders {
        body: &mir::Body
        pointer_bytes: u64
        borrows: Vec~Id~Instruction~~
        holder_starts: Vec~usize~
        loans_through: Vec~Vec~Id~Loan~~~
        levels() first_level() slot_name()
        holder_count() live_set() holders_at()
        value_holders() value_holder() result_holders()
        slot_holders() returned_holders() operand_holders()
        place_holders() argument_holders()
        origin_holders() loans_of() uses() defines()
        loan() parameter_loan() loan_count() lent()
        reserved_loans() Vec~Option~Reservation~~
        lines_that_matter() Vec~Box~[Id~Instruction~]~~
        step_held(held, id)
        step_write(held, place, width, sources, paired, parameter)
        step_live(live, id)
        holder_words(holder) &LoanWords
    }
    class Loans {
        holders: Holders
        reserved: Vec~Option~Reservation~~
        lines: Vec~Box~[Id~Instruction~]~~
        live_at_ends()
        held_at_starts(ends)
        report(file, diagnostics)
        label_later_use()
        return_errors()
        accesses(id) [Option~Access~; 2]
        move_of_read()
        broken_loan()
        activation_error(waiting)
        next_use(holder, block, position) Option~Span~
    }
    class Held {
        pairs: Vec~(Id~Holder~, Id~Loan~)~
        pair_range(holder)
        loans(holder)
        set(holder, loans)
        add(holder, loan)
    }
    class Bytes {
        start: u64
        width: u64
        stride: u64
        count: u64
        span() Range~u64~
        is_solid() bool
        run_meets(range) bool
        meets(other) bool
    }
    class Lent {
        <<enum>>
        Borrow(Borrowed)
        Parameter(usize)
    }
    class Borrowed {
        instruction: Id~Instruction~
        place: PlaceRef
        size: Option~u64~
        mutable: bool
        words: &LoanWords
    }
    class Reservation {
        call: Id~Instruction~
        carrier: Option~Id~Holder~~
    }
    class Access {
        place: PlaceRef
        width: Option~u64~
        subject: &str
        event: &str
        label: &str
        reads: bool
        own_storage: bool
        span: Span
    }

    Loans *-- Holders
    Loans o-- Reservation
    Holders ..> Held : steps change it
    Holders ..> Bytes : levels / first_level
    Holders ..> Lent : lent(loan)
    Lent *-- Borrowed
    Loans ..> Access : accesses(id)
```

Helper functions outside the impls: `holder_ids(range)` (`mod.rs`, used by
the numbering and the passes), `sorted_union(a, b)` (`passes.rs`, only the
passes use it), `known_until_through(steps)` and `names_exactly(place)`
(`places.rs`).

## 5. `loans/`: how one check runs

```mermaid
flowchart TD
    RLE["report_loan_errors(body, pointer_bytes, file, diagnostics)<br/>builds borrows, holder_starts, loans_through"] -->|no Borrow and the result holds no loan| STOP[nothing to check]
    RLE --> H["Holders { ... }"]
    H --> RES["reserved_loans()<br/>which &mut loans wait for a call (two-phase)<br/>uses origin_holders, uses, names_exactly"]
    H --> LTM["lines_that_matter()<br/>per block: only lines that give, use or break a loan<br/>uses accesses, defines, uses, place_holders"]
    RES & LTM --> L["Loans { holders, reserved, lines }"]
    L --> REP["Loans::report(file, diagnostics)"]

    REP --> P1["1. live_at_ends()<br/>backward dataflow: which holders are used later<br/>run = step_live (defines / uses), seed = returned_holders"]
    REP --> P2["2. held_at_starts(ends)<br/>forward dataflow: which loans each holder holds<br/>run = step_held → step_write, seed = parameter_loan per parameter"]
    REP --> P3["3. walk each block's lines"]

    P3 --> ACC["accesses(id): up to 2 Access per line"]
    ACC --> BL["broken_loan(...)<br/>Bytes::meets via levels → T0037 / T0016"]
    P3 --> MOR["move_of_read(...)"]
    P3 --> ACT["activation_error(waiting, ...)<br/>a reserved &mut taking hold at its call"]
    ACT --> BL
    P3 --> RET["return_errors(...)<br/>a returned value holding own storage → T0036"]
    BL --> LLU["label_later_use(...)"]
    LLU --> NU["next_use(holder, block, position)<br/>search forward over self.lines"]
    LLU --> HW["holder_words(holder)"]
```
