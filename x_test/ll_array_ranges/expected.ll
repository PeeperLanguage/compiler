source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [1 x i8] c"\00"
@.str.1 = private unnamed_addr constant [3 x i8] c", \00"
@.str.2 = private unnamed_addr constant [2 x i8] c"[\00"
@.str.3 = private unnamed_addr constant [3 x i8] c"]\0A\00"

define void @"ll_array_ranges:main.known"(ptr %result) !dbg !7 {
entry:
  %v0 = alloca [6 x i32]
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !9, metadata !DIExpression()), !dbg !10
  %i0 = alloca { i32, i32, i32, i32, i1 }
  %i1 = alloca { i32, i32, i32, i32, i1 }
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i0, i32 0, i32 0, !dbg !11
  store i32 0, ptr %i0.0, !dbg !11
  %i0.1 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i0, i32 0, i32 1, !dbg !11
  store i32 3, ptr %i0.1, !dbg !11
  %i0.2 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i0, i32 0, i32 2, !dbg !11
  store i32 1, ptr %i0.2, !dbg !11
  %i0.3 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i0, i32 0, i32 3, !dbg !11
  store i32 1, ptr %i0.3, !dbg !11
  %i0.4 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i0, i32 0, i32 4, !dbg !11
  store i1 false, ptr %i0.4, !dbg !11
  %i1.0 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i1, i32 0, i32 0, !dbg !12
  store i32 5, ptr %i1.0, !dbg !12
  %i1.1 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i1, i32 0, i32 1, !dbg !12
  store i32 1, ptr %i1.1, !dbg !12
  %i1.2 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i1, i32 0, i32 2, !dbg !12
  store i32 1, ptr %i1.2, !dbg !12
  %i1.3 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i1, i32 0, i32 3, !dbg !12
  store i32 -1, ptr %i1.3, !dbg !12
  %i1.4 = getelementptr inbounds { i32, i32, i32, i32, i1 }, ptr %i1, i32 0, i32 4, !dbg !12
  store i1 true, ptr %i1.4, !dbg !12
  %i2.r0.whole = udiv i32 3, 1, !dbg !13
  %i2.r0.wide = zext i32 %i2.r0.whole to i64, !dbg !13
  %i2.r0.rest = urem i32 3, 1, !dbg !13
  %i2.r0.more = icmp ne i32 %i2.r0.rest, 0, !dbg !13
  %i2.r0.last = or i1 false, %i2.r0.more, !dbg !13
  %i2.r0.extra = zext i1 %i2.r0.last to i64, !dbg !13
  %i2.r0.count = add i64 %i2.r0.wide, %i2.r0.extra, !dbg !13
  %i2.r2.whole = udiv i32 1, 1, !dbg !13
  %i2.r2.wide = zext i32 %i2.r2.whole to i64, !dbg !13
  %i2.r2.rest = urem i32 1, 1, !dbg !13
  %i2.r2.more = icmp ne i32 %i2.r2.rest, 0, !dbg !13
  %i2.r2.last = or i1 true, %i2.r2.more, !dbg !13
  %i2.r2.extra = zext i1 %i2.r2.last to i64, !dbg !13
  %i2.r2.count = add i64 %i2.r2.wide, %i2.r2.extra, !dbg !13
  %i2.0 = getelementptr inbounds [6 x i32], ptr %v0, i32 0, i64 0, !dbg !13
  call void @fill.range.0(ptr %i2.0, i32 0, i32 1, i64 %i2.r0.count), !dbg !13
  %i2.o0 = add i64 0, %i2.r0.count, !dbg !13
  %i2.1 = getelementptr inbounds [6 x i32], ptr %v0, i32 0, i64 %i2.o0, !dbg !13
  store i32 9, ptr %i2.1, !dbg !13
  %i2.o1 = add i64 %i2.o0, 1, !dbg !13
  %i2.2 = getelementptr inbounds [6 x i32], ptr %v0, i32 0, i64 %i2.o1, !dbg !13
  call void @fill.range.0(ptr %i2.2, i32 5, i32 -1, i64 %i2.r2.count), !dbg !13
  call void @llvm.memcpy.p0.p0.i64(ptr %result, ptr %v0, i64 24, i1 false), !dbg !15
  ret void, !dbg !16
bb1:
  unreachable
}

define void @"ll_array_ranges:main.main"() !dbg !18 {
entry:
  %i0 = alloca [6 x i32]
  br label %bb0
bb0:
  call void @"ll_array_ranges:main.known"(ptr %i0), !dbg !19
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.2), !dbg !20
  call void @print.array.0(ptr %i0), !dbg !20
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.3), !dbg !20
  call void () @"peeper_rt_v1_flush"(), !dbg !20
  ret void, !dbg !21
}

define private void @fill.range.0(ptr %dest, i32 %start, i32 %delta, i64 %count) {
entry:
  %empty = icmp eq i64 %count, 0
  br i1 %empty, label %done, label %loop
loop:
  %index = phi i64 [ 0, %entry ], [ %next, %loop ]
  %value = phi i32 [ %start, %entry ], [ %after, %loop ]
  %address = getelementptr inbounds i32, ptr %dest, i64 %index
  store i32 %value, ptr %address
  %after = add i32 %value, %delta
  %next = add i64 %index, 1
  %more = icmp ult i64 %next, %count
  br i1 %more, label %loop, label %done
done:
  ret void
}

define private void @print.array.0(ptr %array) {
entry:
  %element.m = alloca i64
  br label %loop
loop:
  %index = phi i64 [ 0, %entry ], [ %next, %step ]
  %first = icmp eq i64 %index, 0
  %separator = select i1 %first, ptr @.str.0, ptr @.str.1
  call void (ptr) @"peeper_rt_v1_print_text"(ptr %separator)
  %address = getelementptr inbounds [6 x i32], ptr %array, i64 0, i64 %index
  %element = load i32, ptr %address
  %element.x = sext i32 %element to i64
  store i64 %element.x, ptr %element.m
  call void (i32, i32, ptr) @"peeper_rt_v2_print_int"(i32 1, i32 1, ptr %element.m)
  br label %step
step:
  %next = add i64 %index, 1
  %more = icmp ult i64 %next, 6
  br i1 %more, label %loop, label %done
done:
  ret void
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @"peeper_rt_v1_flush"()

declare void @"peeper_rt_v1_print_text"(ptr)

declare void @"peeper_rt_v2_print_int"(i32, i32, ptr)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!22, !23}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!3 = !DISubrange(count: 6)
!4 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 192, elements: !{!3})
!5 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !4, size: 64)
!6 = !DISubroutineType(types: !{null, !5})
!7 = distinct !DISubprogram(name: "known", linkageName: "ll_array_ranges:main.known", scope: !0, file: !0, line: 3, type: !6, scopeLine: 3, spFlags: DISPFlagDefinition, unit: !1)
!8 = !DILexicalBlock(scope: !7, file: !0, line: 3, column: 22)
!9 = !DILocalVariable(name: "arr", scope: !8, file: !0, line: 4, type: !4)
!10 = !DILocation(line: 4, column: 9, scope: !8)
!11 = !DILocation(line: 4, column: 23, scope: !8)
!12 = !DILocation(line: 4, column: 37, scope: !8)
!13 = !DILocation(line: 4, column: 15, scope: !8)
!14 = !DILocation(line: 4, column: 5, scope: !8)
!15 = !DILocation(line: 5, column: 12, scope: !8)
!16 = !DILocation(line: 5, column: 5, scope: !8)
!17 = !DISubroutineType(types: !{null})
!18 = distinct !DISubprogram(name: "main", linkageName: "ll_array_ranges:main.main", scope: !0, file: !0, line: 8, type: !17, scopeLine: 8, spFlags: DISPFlagDefinition, unit: !1)
!19 = !DILocation(line: 9, column: 13, scope: !18)
!20 = !DILocation(line: 9, column: 5, scope: !18)
!21 = !DILocation(line: 10, column: 1, scope: !18)
!22 = !{i32 7, !"Dwarf Version", i32 4}
!23 = !{i32 2, !"Debug Info Version", i32 3}
