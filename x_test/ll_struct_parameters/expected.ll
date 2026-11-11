source_filename = "<fixture>/src/main.peep"

define void @"ll_struct_parameters:main.swap"(ptr %a0, ptr %a1) !dbg !10 {
entry:
  call void @llvm.dbg.declare(metadata ptr %a1, metadata !11, metadata !DIExpression()), !dbg !12
  br label %bb0
bb0:
  %i0.a = getelementptr inbounds i8, ptr %a1, i64 4, !dbg !13
  %i0 = load i32, ptr %i0.a, !dbg !13
  store i32 %i0, ptr %a0, !dbg !13
  %i2 = load i32, ptr %a1, !dbg !14
  %i3 = getelementptr inbounds i8, ptr %a0, i64 4, !dbg !14
  store i32 %i2, ptr %i3, !dbg !14
  ret void, !dbg !15
}

define void @"ll_struct_parameters:main.main"() !dbg !17 {
entry:
  %s0 = alloca [8 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !19, metadata !DIExpression()), !dbg !20
  %s1 = alloca [8 x i8], align 4
  br label %bb0
bb0:
  store i32 1, ptr %s1, !dbg !22
  %i2 = getelementptr inbounds i8, ptr %s1, i64 4, !dbg !23
  store i32 2, ptr %i2, !dbg !23
  call void @"ll_struct_parameters:main.swap"(ptr %s0, ptr %s1), !dbg !21
  ret void, !dbg !24
}

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!25, !26}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!4 = !DIDerivedType(tag: DW_TAG_member, name: "x", scope: !2, baseType: !3, offset: 0)
!5 = !DIDerivedType(tag: DW_TAG_member, name: "y", scope: !2, baseType: !3, offset: 32)
!6 = !{!4, !5}
!2 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Point", size: 64, elements: !6)
!3 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!7 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!8 = !DIDerivedType(tag: DW_TAG_pointer_type, baseType: !2, size: 64)
!9 = !DISubroutineType(types: !{null, !7, !8})
!10 = distinct !DISubprogram(name: "swap", linkageName: "ll_struct_parameters:main.swap", scope: !0, file: !0, line: 7, type: !9, scopeLine: 7, spFlags: DISPFlagDefinition, unit: !1)
!11 = !DILocalVariable(name: "p", arg: 1, scope: !10, file: !0, line: 7, type: !2)
!12 = !DILocation(line: 7, column: 9, scope: !10)
!13 = !DILocation(line: 8, column: 24, scope: !10)
!14 = !DILocation(line: 8, column: 33, scope: !10)
!15 = !DILocation(line: 8, column: 5, scope: !10)
!16 = !DISubroutineType(types: !{null})
!17 = distinct !DISubprogram(name: "main", linkageName: "ll_struct_parameters:main.main", scope: !0, file: !0, line: 11, type: !16, scopeLine: 11, spFlags: DISPFlagDefinition, unit: !1)
!18 = !DILexicalBlock(scope: !17, file: !0, line: 11, column: 11)
!19 = !DILocalVariable(name: "p", scope: !18, file: !0, line: 12, type: !2)
!20 = !DILocation(line: 12, column: 9, scope: !18)
!21 = !DILocation(line: 12, column: 13, scope: !18)
!22 = !DILocation(line: 12, column: 30, scope: !18)
!23 = !DILocation(line: 12, column: 37, scope: !18)
!24 = !DILocation(line: 13, column: 1, scope: !18)
!25 = !{i32 7, !"Dwarf Version", i32 4}
!26 = !{i32 2, !"Debug Info Version", i32 3}
