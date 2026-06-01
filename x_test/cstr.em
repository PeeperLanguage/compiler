fn main() -> i32 {
    let msg: cstr = "Hello from Ember runtime ABI!\n";
    let _ = write(stdout, msg, 30);
    return 0;
}
