#[extern]
fn write(fd: i32, buf: cstr, count: i32) -> i32;

fn main() -> i32 {
    let msg: cstr = "Hello from Ember runtime ABI!\n";
    let _ = write(1, msg, 30);
    return 0;
}
