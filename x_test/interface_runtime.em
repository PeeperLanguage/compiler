interface Summer {
    sum(Self): i32,
}

struct Point {
    x: i32,
    y: i32,
}

impl Point {
    fn sum(self: Self) -> i32 {
        return self.x + self.y;
    }
}

fn total(v: Summer) -> i32 {
    return v.sum();
}

fn main() -> i32 {
    let p: Point = .{ x = 10, y = 20 };
    if total(p) == 30 {
        let ok: cstr = "interface ok\n";
        write(stdout, ok, 13);
        return 30;
    }

    let bad: cstr = "interface bad\n";
    write(stdout, bad, 14);
    return 1;
}
