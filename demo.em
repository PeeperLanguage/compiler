import "sys/mem";

 fn main() { 
    println("Hello, World!");

    let myname : str = "Jacob"; // stack allocated
    let myname_copy = myname; // copies all the data to a new location

    myname_copy = "Hello, World!";
    println(myname_copy); // prints "Hello, World!"
    println(myname); // prints "Jacob"

    // pointer types
    let ptr_to_myname : *const str = &const myname; // pointer to the location of myname
    println(ptr_to_myname); // prints the memory address of myname
    // *ptr_to_myname = "something"; // forbidden. Need write access
    let another_ptr : *str = &myname; // mutable pointer to the location of myname
    *another_ptr = "something"; // allowed
    // No writable pointer should be allowed more than once
    // let yet_another_ptr : *str = &myname; // another mutable pointer to the location of myname. forbidden

    // pointer release is manual. Must use drop() to release the memory
    drop(another_ptr); // back to the pool
    // as the data is on stack. Its lifetime is limited to the scope it was created in.

    // dynamic allocation
    const allocator = mem::SystemAllocator();
    let bigText : *const str = allocator<str>("", 1000);
    println(bigText); // prints the memory address of the allocated memory
    println(*bigText); // prints the contents of the allocated memory
    let bigText_copy = bigText; // pointer is copied, not the contents. So possible dangling pointer
    drop(bigText); // release the memory back to the pool
 }