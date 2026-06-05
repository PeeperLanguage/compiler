import "external";

type unused_private_type i32;

fn unused_private_func() -> i32 {
    let unused_local: i32 = 10;
    let _ignored_local: i32 = 20;
    return 0;
}

fn UnusedPublicFunction() -> i32 {
    return 100;
}

fn main(unused_param: i32, _ignored_param: i32) -> i32 {
    let msg: cstr = "Hello from Ember runtime ABI!\n";
    write(stdout, msg, 30);
    return 0;
}
