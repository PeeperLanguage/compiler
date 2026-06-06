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

impl i32 {
    fn to_float(self: Self) -> f32 {
        return (self+1) as f32;
    }
}

impl i32 {
    fn to_str(_self: ^Self) -> cstr {
        return "converted\n";
    }
}

struct Counter {
    value: i32,
}

impl Counter {
    fn bump(self: ^Self) -> i32 {
        self.value = self.value + 1;
        return self.value;
    }
}


fn main() -> i32 {
    let p: Point = .{ x = 10, y = 20 };
    if total(p) == 30 {
        let ok: cstr = "interface ok\n";
        write(stdout, ok, 13);
        let mut i: i32 = 42;
        let f: f32 = i.to_float();
        if f == 43.0 {
            let s: cstr = "float ok\n";
            write(stdout, s, 9);
        }
    
        let s: cstr = i.to_str();
        write(stdout, s, 11);

        let mut c: Counter = .{ value = 0 };
        let v: i32 = c.bump();
        if c.value == v {
            let msg: cstr = "both updated\n";
            write(stdout, msg, 13);
        } else {
            let msg: cstr = "only value updated\n";
            write(stdout, msg, 19);
        }

        c.value = 100;
        if c.value == 100 {
            let msg: cstr = "value updated to 100\n";
            write(stdout, msg, 21);
        }
        
    
        return 30;
    }

    let bad: cstr = "interface bad\n";
    write(stdout, bad, 14);


    return 1;
}
