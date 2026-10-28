source_filename = "<fixture>/src/main.peep"

%slice = type { ptr, i64 }

@.str.0 = private unnamed_addr constant [66 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:5:12\0A\00"
@.str.1 = private unnamed_addr constant [65 x i8] c"panic: index out of bounds of the slice\0A  --> src/main.peep:9:5\0A\00"

define i32 @"ll_slices:main.get"(%slice %arg0, i64 %arg1) !dbg !11 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !12, metadata !DIExpression()), !dbg !13
  %v1 = alloca i64
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !14, metadata !DIExpression()), !dbg !15
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !13
  store i64 %arg1, ptr %v1, !dbg !15
  %i2 = load i64, ptr %v1, !dbg !16
  %i3.f.0.f = getelementptr inbounds %slice, ptr %v0, i32 0, !dbg !17
  %i3.f.0.s = load %slice, ptr %i3.f.0.f, !dbg !17
  %i3.f.0 = extractvalue %slice %i3.f.0.s, 0, !dbg !17
  %i3.f.0.len = extractvalue %slice %i3.f.0.s, 1, !dbg !17
  %i3.f.1.0.out = icmp uge i64 %i2, %i3.f.0.len, !dbg !17
  br i1 %i3.f.1.0.out, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i3.f = getelementptr inbounds i32, ptr %i3.f.0, i64 %i2, !dbg !17
  %i3 = load i32, ptr %i3.f, !dbg !17
  %i4 = load %slice, ptr %v0, !dbg !18
  %i5 = extractvalue %slice %i4, 1, !dbg !18
  %i6 = trunc i64 %i5 to i32, !dbg !18
  %i7 = add i32 %i3, %i6, !dbg !17
  ret i32 %i7, !dbg !19
bb1:
  unreachable
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.0, i32 65), !dbg !17
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !17
  unreachable
}

define void @"ll_slices:main.set"(%slice %arg0) !dbg !27 {
entry:
  %v0 = alloca %slice
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !28, metadata !DIExpression()), !dbg !29
  br label %bb0
bb0:
  store %slice %arg0, ptr %v0, !dbg !29
  %i1.f.0.f = getelementptr inbounds %slice, ptr %v0, i32 0, !dbg !30
  %i1.f.0.s = load %slice, ptr %i1.f.0.f, !dbg !30
  %i1.f.0 = extractvalue %slice %i1.f.0.s, 0, !dbg !30
  %i1.f.0.len = extractvalue %slice %i1.f.0.s, 1, !dbg !30
  %i1.f.1.0.out = icmp uge i64 0, %i1.f.0.len, !dbg !30
  br i1 %i1.f.1.0.out, label %bb0.1.panic, label %bb0.1
bb0.1:
  %i1.f = getelementptr inbounds i32, ptr %i1.f.0, i64 0, !dbg !30
  store i32 1, ptr %i1.f, !dbg !30
  ret void, !dbg !31
bb0.1.panic:
  call i32 @"peeper_rt_v1_write"(i32 2, ptr @.str.1, i32 64), !dbg !30
  call void @"peeper_rt_v1_exit"(i32 101), !dbg !30
  unreachable
}

define void @"ll_slices:main.main"() !dbg !33 {
entry:
  %v0 = alloca [2 x i32]
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !37, metadata !DIExpression()), !dbg !38
  %v1 = alloca i32
  call void @llvm.dbg.declare(metadata ptr %v1, metadata !39, metadata !DIExpression()), !dbg !40
  br label %bb0
bb0:
  %i0.0 = getelementptr inbounds [2 x i32], ptr %v0, i32 0, i64 0, !dbg !41
  store i32 3, ptr %i0.0, !dbg !41
  %i0.1 = getelementptr inbounds [2 x i32], ptr %v0, i32 0, i64 1, !dbg !41
  store i32 4, ptr %i0.1, !dbg !41
  %i2.p = getelementptr inbounds [2 x i32], ptr %v0, i32 0, !dbg !43
  %i2.a = insertvalue %slice poison, ptr %i2.p, 0, !dbg !43
  %i2 = insertvalue %slice %i2.a, i64 2, 1, !dbg !43
  call void @"ll_slices:main.set"(%slice %i2), !dbg !44
  %i4.p = getelementptr inbounds [2 x i32], ptr %v0, i32 0, !dbg !45
  %i4.a = insertvalue %slice poison, ptr %i4.p, 0, !dbg !45
  %i4 = insertvalue %slice %i4.a, i64 2, 1, !dbg !45
  %i5 = call i32 @"ll_slices:main.get"(%slice %i4, i64 1), !dbg !46
  store i32 %i5, ptr %v1, !dbg !47
  ret void, !dbg !48
}

declare void @"peeper_rt_v1_exit"(i32)

declare i32 @"peeper_rt_v1_write"(i32, ptr, i32)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!49, !50}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!4 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!5 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!6 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !3, baseType: !4, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !3, baseType: !5, offset: 64)
!8 = !{!6, !7}
!3 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "[&]i32", size: 128, elements: !8)
!9 = !DIBasicType(name: "u64", size: 64, encoding: DW_ATE_unsigned)
!10 = !DISubroutineType(types: !{!2, !3, !9})
!11 = distinct !DISubprogram(name: "get", linkageName: "ll_slices:main.get", scope: !0, file: !0, line: 4, type: !10, scopeLine: 4, spFlags: DISPFlagDefinition, unit: !1)
!12 = !DILocalVariable(name: "view", arg: 1, scope: !11, file: !0, line: 4, type: !3)
!13 = !DILocation(line: 4, column: 8, scope: !11)
!14 = !DILocalVariable(name: "at", arg: 2, scope: !11, file: !0, line: 4, type: !9)
!15 = !DILocation(line: 4, column: 22, scope: !11)
!16 = !DILocation(line: 5, column: 17, scope: !11)
!17 = !DILocation(line: 5, column: 12, scope: !11)
!18 = !DILocation(line: 5, column: 23, scope: !11)
!19 = !DILocation(line: 5, column: 5, scope: !11)
!21 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!22 = !DIBasicType(name: "usize", size: 64, encoding: DW_ATE_unsigned)
!23 = !DIDerivedType(tag: DW_TAG_member, name: "ptr", scope: !20, baseType: !21, offset: 0)
!24 = !DIDerivedType(tag: DW_TAG_member, name: "len", scope: !20, baseType: !22, offset: 64)
!25 = !{!23, !24}
!20 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "[&mut]i32", size: 128, elements: !25)
!26 = !DISubroutineType(types: !{null, !20})
!27 = distinct !DISubprogram(name: "set", linkageName: "ll_slices:main.set", scope: !0, file: !0, line: 8, type: !26, scopeLine: 8, spFlags: DISPFlagDefinition, unit: !1)
!28 = !DILocalVariable(name: "edit", arg: 1, scope: !27, file: !0, line: 8, type: !20)
!29 = !DILocation(line: 8, column: 8, scope: !27)
!30 = !DILocation(line: 9, column: 5, scope: !27)
!31 = !DILocation(line: 10, column: 1, scope: !27)
!32 = !DISubroutineType(types: !{null})
!33 = distinct !DISubprogram(name: "main", linkageName: "ll_slices:main.main", scope: !0, file: !0, line: 12, type: !32, scopeLine: 12, spFlags: DISPFlagDefinition, unit: !1)
!34 = !DILexicalBlock(scope: !33, file: !0, line: 12, column: 11)
!35 = !DISubrange(count: 2)
!36 = !DICompositeType(tag: DW_TAG_array_type, baseType: !2, size: 64, elements: !{!35})
!37 = !DILocalVariable(name: "values", scope: !34, file: !0, line: 13, type: !36)
!38 = !DILocation(line: 13, column: 13, scope: !34)
!39 = !DILocalVariable(name: "read", scope: !34, file: !0, line: 15, type: !2)
!40 = !DILocation(line: 15, column: 9, scope: !34)
!41 = !DILocation(line: 13, column: 22, scope: !34)
!42 = !DILocation(line: 13, column: 5, scope: !34)
!43 = !DILocation(line: 14, column: 9, scope: !34)
!44 = !DILocation(line: 14, column: 5, scope: !34)
!45 = !DILocation(line: 15, column: 20, scope: !34)
!46 = !DILocation(line: 15, column: 16, scope: !34)
!47 = !DILocation(line: 15, column: 5, scope: !34)
!48 = !DILocation(line: 16, column: 1, scope: !34)
!49 = !{i32 7, !"Dwarf Version", i32 4}
!50 = !{i32 2, !"Debug Info Version", i32 3}
