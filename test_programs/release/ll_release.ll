source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [7 x i8] c"twice\0A\00"

define i32 @"ll_release:main.twice"(i32 %a0) {
entry:
  %s0 = alloca [4 x i8], align 4
  %s1 = alloca [4 x i8], align 4
  br label %bb0
bb0:
  store i32 %a0, ptr %s0
  %i1 = load i32, ptr %s0
  %i2 = mul i32 %i1, 2
  store i32 %i2, ptr %s1
  %i4 = load i32, ptr %s1
  ret i32 %i4
}

define i32 @"ll_release:main.main"() {
entry:
  %s0 = alloca [4 x i8], align 4
  br label %bb0
bb0:
  %i0 = call i32 @"peeper_rt_v1_write"(i32 1, ptr @.str.0, i32 6)
  %i1 = call i32 @"ll_release:main.twice"(i32 2)
  store i32 %i1, ptr %s0
  %i3 = load i32, ptr %s0
  %i4.c = icmp slt i32 3, %i3
  br i1 %i4.c, label %bb1, label %bb2
bb1:
  ret i32 0
bb2:
  ret i32 1
}

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

