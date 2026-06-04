let stdin:  i32 = 0;
let stdout: i32 = 1;
let stderr: i32 = 2;

#[extern] 
fn write(fd: i32, buf: cstr, n: i32) -> i32;

#[extern] 
fn read(fd: i32,  buf: cstr, n: i32) -> i32;

#[extern] 
fn exit(code: i32);
