source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [5 x i8] c"true\00"
@.str.1 = private unnamed_addr constant [6 x i8] c"false\00"
@.str.2 = private unnamed_addr constant [2 x i8] c"\0A\00"

define ptr @"ll_pointer_offsets:main.Next"(ptr %arg0, i64 %arg1) {
entry:
  %v0 = alloca ptr
  %v1 = alloca i64
  br label %bb0
bb0:
  store ptr %arg0, ptr %v0
  store i64 %arg1, ptr %v1
  %i2 = load ptr, ptr %v0
  %i3 = load i64, ptr %v1
  %i4 = getelementptr i8, ptr %i2, i64 %i3
  ret ptr %i4
}

define void @"ll_pointer_offsets:main.main"() {
entry:
  br label %bb0
bb0:
  %i0 = call ptr @"ll_pointer_offsets:main.Next"(ptr null, i64 8)
  %i1 = icmp eq ptr %i0, null
  %i2.0.b = select i1 %i1, ptr @.str.0, ptr @.str.1
  call void @"peeper_rt_v1_print_text"(ptr %i2.0.b)
  call void @"peeper_rt_v1_print_bytes"(ptr @.str.2, i64 1)
  call void @"peeper_rt_v1_flush"()
  ret void
}

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_bytes"(ptr, i64)

declare void @"peeper_rt_v1_print_text"(ptr)

