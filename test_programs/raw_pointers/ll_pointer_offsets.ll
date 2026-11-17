source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [5 x i8] c"true\00"
@.str.1 = private unnamed_addr constant [6 x i8] c"false\00"
@.str.2 = private unnamed_addr constant [2 x i8] c"\0A\00"

define ptr @"ll_pointer_offsets:main.Next"(ptr %a0, i64 %a1) {
entry:
  %s0 = alloca [8 x i8], align 8
  %s1 = alloca [8 x i8], align 8
  br label %bb0
bb0:
  store ptr %a0, ptr %s0
  store i64 %a1, ptr %s1
  %i2 = load ptr, ptr %s0
  %i3 = load i64, ptr %s1
  %i4 = getelementptr i8, ptr %i2, i64 %i3
  ret ptr %i4
}

define void @"ll_pointer_offsets:main.main"() {
entry:
  br label %bb0
bb0:
  %i0 = call ptr @"ll_pointer_offsets:main.Next"(ptr null, i64 8)
  %i1.c = icmp eq ptr %i0, null
  br i1 %i1.c, label %bb1, label %bb2
bb1:
  call void @"peeper_rt_v1_print_text"(ptr @.str.0)
  br label %bb3
bb2:
  call void @"peeper_rt_v1_print_text"(ptr @.str.1)
  br label %bb3
bb3:
  call void @"peeper_rt_v1_print_text"(ptr @.str.2)
  call void @"peeper_rt_v1_flush"()
  ret void
}

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_text"(ptr)

