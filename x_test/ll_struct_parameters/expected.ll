source_filename = "<fixture>/src/main.peep"

define { i32, i32 } @"ll_struct_parameters:main.swap"({ i32, i32 } %arg0) !dbg !8 {
entry:
  %v0 = alloca { i32, i32 }
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !9, metadata !DIExpression()), !dbg !10
  br label %bb0
bb0:
  store { i32, i32 } %arg0, ptr %v0, !dbg !10
  %i1.f = getelementptr inbounds { i32, i32 }, ptr %v0, i32 0, i32 1, !dbg !11
  %i1 = load i32, ptr %i1.f, !dbg !11
  %i2.f = getelementptr inbounds { i32, i32 }, ptr %v0, i32 0, i32 0, !dbg !12
  %i2 = load i32, ptr %i2.f, !dbg !12
  %i3.0 = insertvalue { i32, i32 } poison, i32 %i1, 0, !dbg !13
  %i3 = insertvalue { i32, i32 } %i3.0, i32 %i2, 1, !dbg !13
  ret { i32, i32 } %i3, !dbg !14
bb1:
  unreachable
}

define void @"ll_struct_parameters:main.main"() !dbg !16 {
entry:
  %v0 = alloca { i32, i32 }
  call void @llvm.dbg.declare(metadata ptr %v0, metadata !18, metadata !DIExpression()), !dbg !19
  br label %bb0
bb0:
  %i0.0 = insertvalue { i32, i32 } poison, i32 1, 0, !dbg !20
  %i0 = insertvalue { i32, i32 } %i0.0, i32 2, 1, !dbg !20
  %i1 = call { i32, i32 } @"ll_struct_parameters:main.swap"({ i32, i32 } %i0), !dbg !21
  store { i32, i32 } %i1, ptr %v0, !dbg !22
  ret void, !dbg !23
}

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!24, !25}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!3 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!4 = !DIDerivedType(tag: DW_TAG_member, name: "x", scope: !2, baseType: !3, offset: 0)
!5 = !DIDerivedType(tag: DW_TAG_member, name: "y", scope: !2, baseType: !3, offset: 32)
!6 = !{!4, !5}
!2 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Point", size: 64, elements: !6)
!7 = !DISubroutineType(types: !{!2, !2})
!8 = distinct !DISubprogram(name: "swap", linkageName: "ll_struct_parameters:main.swap", scope: !0, file: !0, line: 5, type: !7, scopeLine: 5, spFlags: DISPFlagDefinition, unit: !1)
!9 = !DILocalVariable(name: "p", arg: 1, scope: !8, file: !0, line: 5, type: !2)
!10 = !DILocation(line: 5, column: 9, scope: !8)
!11 = !DILocation(line: 6, column: 24, scope: !8)
!12 = !DILocation(line: 6, column: 33, scope: !8)
!13 = !DILocation(line: 6, column: 12, scope: !8)
!14 = !DILocation(line: 6, column: 5, scope: !8)
!15 = !DISubroutineType(types: !{null})
!16 = distinct !DISubprogram(name: "main", linkageName: "ll_struct_parameters:main.main", scope: !0, file: !0, line: 9, type: !15, scopeLine: 9, spFlags: DISPFlagDefinition, unit: !1)
!17 = !DILexicalBlock(scope: !16, file: !0, line: 9, column: 11)
!18 = !DILocalVariable(name: "p", scope: !17, file: !0, line: 10, type: !2)
!19 = !DILocation(line: 10, column: 9, scope: !17)
!20 = !DILocation(line: 10, column: 18, scope: !17)
!21 = !DILocation(line: 10, column: 13, scope: !17)
!22 = !DILocation(line: 10, column: 5, scope: !17)
!23 = !DILocation(line: 11, column: 1, scope: !17)
!24 = !{i32 7, !"Dwarf Version", i32 4}
!25 = !{i32 2, !"Debug Info Version", i32 3}
