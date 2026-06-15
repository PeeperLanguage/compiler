struct Point {
    x: i32,
    y: i32,
}

impl Point {
    fn sum(self: Self) -> i32 {
        return self.x + self.y;
    }
}


// program entrypoint
fn main() -> i32 {
    let p: Point = .Point{ x = 10, y = 20 };
    if p.sum() == 30 {
        let ok: cstr = "struct ok\n";
        write(stdout, ok, 10);
        return p.x;
    }

    // if p.sum() {
    //     let ok: cstr = "struct ok\n";
    //     write(stdout, ok, 10);
    //     return p.x;
    // }

    let bad: cstr = "struct bad\n";
    write(stdout, bad, 11);
    return 1;
}
