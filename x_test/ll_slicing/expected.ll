source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [118 x i8] c"panic: index out of bounds of the slice\0A  --> <fixture>/src/main.peep:6:12\0A\00"
@.str.1 = private unnamed_addr constant [106 x i8] c"panic: slice out of bounds\0A  --> <fixture>/src/main.peep:13:32\0A\00"
@.str.2 = private unnamed_addr constant [113 x i8] c"panic: slice starts after it ends\0A  --> <fixture>/src/main.peep:13:24\0A\00"
@.str.3 = private unnamed_addr constant [106 x i8] c"panic: slice out of bounds\0A  --> <fixture>/src/main.peep:14:36\0A\00"

define i32 @"ll_slicing:main.total"(%slice %arg0) !dbg !10 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !11, metadata !DIExpression()), !dbg !12
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !12
  %i1.f.0.f = getelementptr inbounds %slice, ptr %v0, i32 0, !dbg !13
  %i1.f.0.s = load %slice, ptr %i1.f.0.f, !dbg !13
  %i1.f.0 = extractvalue %slice %i1.f.0.s, 0, !dbg !13
  %i1.f.0.len = extractvalue %slice %i1.f.0.s, 1, !dbg !13
  %i1.f.1.0.out = icmp uge i64 0, %i1.f.0.len, !dbg !13
  br i1 %i1.f.1.0.out, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i1.f = getelementptr inbounds i32, ptr %i1.f.0, i64 0, !dbg !13
  %i1 = load i32, ptr %i1.f, !dbg !13
  %i2 = load %slice, ptr %v0, !dbg !14
  %i3 = extractvalue %slice %i2, 1, !dbg !14
  %i4 = trunc i64 %i3 to i32, !dbg !14
  %i5 = add i32 %i1, %i4, !dbg !13
  ret i32 %i5, !dbg !15
bb1:
  unreachable
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.0, i32 117), !dbg !13
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !13
  unreachable
}

define void @"ll_slicing:main.main"() !dbg !17 {
entry:
  %v0 = alloca [4 x i32]
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !21, metadata !DIExpression()), !dbg !22
  %v1 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !23, metadata !DIExpression()), !dbg !24
  %v2 = alloca i64
  call void @llvm.dbg.declare(metadata ptr %v2, metadata !26, metadata !DIExpression()), !dbg !27
  %v3 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v3, metadata !28, metadata !DIExpression()), !dbg !29
  %v4 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v4, metadata !30, metadata !DIExpression()), !dbg !31
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 0, !dbg !32
  store i32 1, ptr %i0.0, !dbg !32
  %i0.1 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 1, !dbg !32
  store i32 2, ptr %i0.1, !dbg !32
  %i0.2 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 2, !dbg !32
  store i32 3, ptr %i0.2, !dbg !32
  %i0.3 = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 3, !dbg !32
  store i32 4, ptr %i0.3, !dbg !32
  store i32 1, ptr %v1, !dbg !34
  store i64 2, ptr %v2, !dbg !35
  %i4 = load i32, ptr %v1, !dbg !36
  %i5 = sext i32 %i4 to i64, !dbg !36
  %i6 = icmp ugt i64 %i5, 4, !dbg !36
  br i1 %i6, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i8 = icmp ugt i64 %i5, 3, !dbg !37
  br i1 %i8, label %bb0.2.panic, label %bb0.2
bb0.2:
  %i10 = sub i64 3, %i5, !dbg !37
  %i11.p = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 %i5, !dbg !37
  %i11.a = insertvalue %slice poison, ptr %i11.p, 0, !dbg !37
  %i11 = insertvalue %slice %i11.a, i64 %i10, 1, !dbg !37
  %i12 = call i32 @"ll_slicing:main.total"(%slice %i11), !dbg !38
  store i32 %i12, ptr %v3, !dbg !39
  %i14 = load i64, ptr %v2, !dbg !40
  %i15 = icmp uge i64 %i14, 4, !dbg !40
  br i1 %i15, label %bb0.3.panic, label %bb0.3
bb0.3:
  %i17 = add i64 %i14, 1, !dbg !41
  %i18.p = getelementptr inbounds [4 x i32], ptr %v0, i32 0, i64 0, !dbg !41
  %i18.a = insertvalue %slice poison, ptr %i18.p, 0, !dbg !41
  %i18 = insertvalue %slice %i18.a, i64 %i17, 1, !dbg !41
  %i19 = call i32 @"ll_slicing:main.total"(%slice %i18), !dbg !42
  store i32 %i19, ptr %v4, !dbg !43
  ret void, !dbg !44
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.1, i32 105), !dbg !36
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !36
  unreachable
bb0.2.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.2, i32 112), !dbg !37
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !37
  unreachable
bb0.3.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.3, i32 105), !dbg !40
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !40
  unreachable
}

declare void @"peeper_rt_v1_exit"(i32)

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!45, !46}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!4 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!5 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!6 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !3, baseType: !4, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !3, baseType: !5, offset: 64)
!8 = !{!6, !7}
!3 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "[&]i32", size: 128, elements: !8)
!9 = !DISubroutineType(types: !{!2, !3})
!10 = distinct !DISubprogram(name: "total", linkageName: "ll_slicing:main.total", scope: !0, file: !0, line: 5, type: !9, scopeLine: 5, spFlags: DISPFlagDefinition, unit: !1)
!11 = !DILocalVariable(name: "view", arg: 1, scope: !10, file: !0, line: 5, type: !3)
!12 = !DILocation(line: 5, column: 10, scope: !10)
!13 = !DILocation(line: 6, column: 12, scope: !10)
!14 = !DILocation(line: 6, column: 22, scope: !10)
!15 = !DILocation(line: 6, column: 5, scope: !10)
!16 = !DISubroutineType(types: !{null})
!17 = distinct !DISubprogram(name: "main", linkageName: "ll_slicing:main.main", scope: !0, file: !0, line: 9, type: !16, scopeLine: 9, spFlags: DISPFlagDefinition, unit: !1)
!18 = !DILexicalBlock(scope: !17, file: !0, line: 9, column: 11)
!19 = !DISubrange(count: 4)
!20 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 128, elements: !{!19})
!21 = !DILocalVariable(name: "values", scope: !18, file: !0, line: 10, type: !20)
!22 = !DILocation(line: 10, column: 9, scope: !18)
!23 = !DILocalVariable(name: "start", scope: !18, file: !0, line: 11, type: !2)
!24 = !DILocation(line: 11, column: 9, scope: !18)
!25 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!26 = !DILocalVariable(name: "end", scope: !18, file: !0, line: 12, type: !25)
!27 = !DILocation(line: 12, column: 9, scope: !18)
!28 = !DILocalVariable(name: "middle", scope: !18, file: !0, line: 13, type: !2)
!29 = !DILocation(line: 13, column: 9, scope: !18)
!30 = !DILocalVariable(name: "through", scope: !18, file: !0, line: 14, type: !2)
!31 = !DILocation(line: 14, column: 9, scope: !18)
!32 = !DILocation(line: 10, column: 18, scope: !18)
!33 = !DILocation(line: 10, column: 5, scope: !18)
!34 = !DILocation(line: 11, column: 5, scope: !18)
!35 = !DILocation(line: 12, column: 5, scope: !18)
!36 = !DILocation(line: 13, column: 32, scope: !18)
!37 = !DILocation(line: 13, column: 24, scope: !18)
!38 = !DILocation(line: 13, column: 18, scope: !18)
!39 = !DILocation(line: 13, column: 5, scope: !18)
!40 = !DILocation(line: 14, column: 36, scope: !18)
!41 = !DILocation(line: 14, column: 25, scope: !18)
!42 = !DILocation(line: 14, column: 19, scope: !18)
!43 = !DILocation(line: 14, column: 5, scope: !18)
!44 = !DILocation(line: 15, column: 1, scope: !18)
!45 = !{i32 7, !"Dwarf Version", i32 4}
!46 = !{i32 2, !"Debug Info Version", i32 3}
