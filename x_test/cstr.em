
fn sayhi() {
    let hi: cstr = "Hello, world!\n";
    write(stdout, hi, 14);
}

fn main() -> i32 {
    let msg: cstr = "Hello from Ember runtime ABI!\n";
    write(stdout, msg, 30);
    sayhi();
    return 0;
}
