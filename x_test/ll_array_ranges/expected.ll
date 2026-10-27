source_filename = "<fixture>/src/main.peep"

@.str.0 = private unnamed_addr constant [1 x i8] c"\00"
@.str.1 = private unnamed_addr constant [3 x i8] c", \00"
@.str.2 = private unnamed_addr constant [2 x i8] c"[\00"
@.str.3 = private unnamed_addr constant [3 x i8] c"]\0A\00"

define void @"ll_array_ranges:main.known"(ptr %result) !dbg !7 {
entry:
  %v0 = alloca [6 x i32]
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !9, metadata !DIExpression()), !dbg !10
  br label %bb0
bb0:
  %i0.r0.whole = udiv i32 3, 1, !dbg !11
  %i0.r0.wide = zext i32 %i0.r0.whole to i64, !dbg !11
  %i0.r0.rest = urem i32 3, 1, !dbg !11
  %i0.r0.more = icmp ne i32 %i0.r0.rest, 0, !dbg !11
  %i0.r0.extra = zext i1 %i0.r0.more to i64, !dbg !11
  %i0.r0.count = add i64 %i0.r0.wide, %i0.r0.extra, !dbg !11
  %i0.r2.whole = udiv i32 1, 1, !dbg !11
  %i0.r2.wide = zext i32 %i0.r2.whole to i64, !dbg !11
  %i0.r2.count = add i64 %i0.r2.wide, 1, !dbg !11
  %i0.0 = getelementptr inbounds [6 x i32], ptr %v0, i32 0, i64 0, !dbg !11
  call void @fill.range.0(ptr %i0.0, i32 0, i32 1, i64 %i0.r0.count), !dbg !11
  %i0.o0 = add i64 0, %i0.r0.count, !dbg !11
  %i0.1 = getelementptr inbounds [6 x i32], ptr %v0, i32 0, i64 %i0.o0, !dbg !11
  store i32 9, ptr %i0.1, !dbg !11
  %i0.o1 = add i64 %i0.o0, 1, !dbg !11
  %i0.2 = getelementptr inbounds [6 x i32], ptr %v0, i32 0, i64 %i0.o1, !dbg !11
  call void @fill.range.0(ptr %i0.2, i32 5, i32 -1, i64 %i0.r2.count), !dbg !11
  call void @llvm.memcpy.p0.p0.i64(ptr %result, ptr %v0, i64 24, i1 false), !dbg !13
  ret void, !dbg !14
bb1:
  unreachable
}

define void @"ll_array_ranges:main.main"() !dbg !16 {
entry:
  %i0 = alloca [6 x i32]
  br label %bb0
bb0:
  call void @"ll_array_ranges:main.known"(ptr %i0), !dbg !17
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.2), !dbg !18
  call void @print.array.0(ptr %i0), !dbg !18
  call void (ptr) @"peeper_rt_v1_print_text"(ptr @.str.3), !dbg !18
  call void () @"peeper_rt_v1_flush"(), !dbg !18
  ret void, !dbg !19
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
  br label %loop
loop:
  %index = phi i64 [ 0, %entry ], [ %next, %step ]
  %first = icmp eq i64 %index, 0
  %separator = select i1 %first, ptr @.str.0, ptr @.str.1
  call void (ptr) @"peeper_rt_v1_print_text"(ptr %separator)
  %address = getelementptr inbounds [6 x i32], ptr %array, i64 0, i64 %index
  %element = load i32, ptr %address
  %element.x = sext i32 %element to i64
  call void (i32, i32, ...) @"peeper_rt_v1_print_int"(i32 1, i32 1, i64 %element.x)
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

declare void @"peeper_rt_v1_print_int"(i32, i32, ...)

declare void @"peeper_rt_v1_print_text"(ptr)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!20, !21}
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
!11 = !DILocation(line: 4, column: 15, scope: !8)
!12 = !DILocation(line: 4, column: 5, scope: !8)
!13 = !DILocation(line: 5, column: 12, scope: !8)
!14 = !DILocation(line: 5, column: 5, scope: !8)
!15 = !DISubroutineType(types: !{null})
!16 = distinct !DISubprogram(name: "main", linkageName: "ll_array_ranges:main.main", scope: !0, file: !0, line: 8, type: !15, scopeLine: 8, spFlags: DISPFlagDefinition, unit: !1)
!17 = !DILocation(line: 9, column: 13, scope: !16)
!18 = !DILocation(line: 9, column: 5, scope: !16)
!19 = !DILocation(line: 10, column: 1, scope: !16)
!20 = !{i32 7, !"Dwarf Version", i32 4}
!21 = !{i32 2, !"Debug Info Version", i32 3}
