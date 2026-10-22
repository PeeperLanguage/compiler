source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [7 x i8] c"twice\0A\00"

define i32 @"ll_release:main.twice"(i32 %arg0) {
entry:
  %v0 = alloca i32
  %v1 = alloca i32
  br label %bb0
bb0:
  store i32 %arg0, ptr %v0
  %i1 = load i32, ptr %v0
  %i2 = mul i32 %i1, 2
  store i32 %i2, ptr %v1
  %i4 = load i32, ptr %v1
  ret i32 %i4
bb1:
  unreachable
}

define i32 @"ll_release:main.main"() {
entry:
  %v0 = alloca i32
  br label %bb0
bb0:
  %i0 = call i32 @"peeper_rt_v1_write"(i32 1, ptr @.str.0, i32 6)
  %i1 = call i32 @"ll_release:main.twice"(i32 2)
  store i32 %i1, ptr %v0
  %i3 = load i32, ptr %v0
  %i4 = icmp sgt i32 %i3, 3
  br i1 %i4, label %bb1, label %bb2
bb1:
  ret i32 0
bb2:
  ret i32 1
bb3:
  br label %bb2
bb4:
  unreachable
}

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

