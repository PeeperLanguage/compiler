source_filename = "<fixture>/src/main.peep"

define void @"ll_struct:main.main"() !dbg !3 {
entry:
  %s0 = alloca [8 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s0, metadata !9, metadata !DIExpression()), !dbg !10
  %s1 = alloca [16 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s1, metadata !15, metadata !DIExpression()), !dbg !16
  %s2 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s2, metadata !17, metadata !DIExpression()), !dbg !18
  %s3 = alloca [4 x i8], align 4
  call void @llvm.dbg.declare(metadata ptr %s3, metadata !19, metadata !DIExpression()), !dbg !20
  %s4 = alloca [8 x i8], align 4
  br label %bb0
bb0:
  %i0 = getelementptr inbounds i8, ptr %s0, i64 4, !dbg !21
  store i32 2, ptr %i0, !dbg !21
  store i32 1, ptr %s0, !dbg !22
  call void @llvm.memcpy.p0.p0.i64(ptr %s1, ptr %s0, i64 8, i1 false), !dbg !23
  %i4 = getelementptr inbounds i8, ptr %s1, i64 8, !dbg !24
  store i32 3, ptr %i4, !dbg !24
  %i5 = getelementptr inbounds i8, ptr %s1, i64 12, !dbg !25
  store i32 4, ptr %i5, !dbg !25
  %i6.a = getelementptr inbounds i8, ptr %s1, i64 8, !dbg !26
  %i6 = load i32, ptr %i6.a, !dbg !26
  store i32 %i6, ptr %s2, !dbg !27
  store i32 5, ptr %s4, !dbg !28
  %i9 = getelementptr inbounds i8, ptr %s4, i64 4, !dbg !29
  store i32 6, ptr %i9, !dbg !29
  %i10.a = getelementptr inbounds i8, ptr %s4, i64 4, !dbg !30
  %i10 = load i32, ptr %i10.a, !dbg !30
  store i32 %i10, ptr %s3, !dbg !31
  ret void, !dbg !32
}

declare void @llvm.memcpy.p0.p0.i64(ptr, ptr, i64, i1)

declare void @llvm.dbg.declare(metadata, metadata, metadata)

!llvm.dbg.cu = !{!1}
!llvm.module.flags = !{!33, !34}
!0 = !DIFile(filename: "main.peep", directory: "<fixture>/src")
!1 = distinct !DICompileUnit(language: DW_LANG_C, file: !0, producer: "peeper", isOptimized: false, runtimeVersion: 0, emissionKind: FullDebug)
!2 = !DISubroutineType(types: !{null})
!3 = distinct !DISubprogram(name: "main", linkageName: "ll_struct:main.main", scope: !0, file: !0, line: 16, type: !2, scopeLine: 16, spFlags: DISPFlagDefinition, unit: !1)
!6 = !DIDerivedType(tag: DW_TAG_member, name: "x", scope: !4, baseType: !5, offset: 0)
!7 = !DIDerivedType(tag: DW_TAG_member, name: "y", scope: !4, baseType: !5, offset: 32)
!8 = !{!6, !7}
!4 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Point", size: 64, elements: !8)
!5 = !DIBasicType(name: "i32", size: 32, encoding: DW_ATE_signed)
!9 = !DILocalVariable(name: "p", scope: !3, file: !0, line: 17, type: !4)
!10 = !DILocation(line: 17, column: 9, scope: !3)
!12 = !DIDerivedType(tag: DW_TAG_member, name: "start", scope: !11, baseType: !4, offset: 0)
!13 = !DIDerivedType(tag: DW_TAG_member, name: "end", scope: !11, baseType: !4, offset: 64)
!14 = !{!12, !13}
!11 = distinct !DICompositeType(tag: DW_TAG_structure_type, name: "Segment", size: 128, elements: !14)
!15 = !DILocalVariable(name: "s", scope: !3, file: !0, line: 18, type: !11)
!16 = !DILocation(line: 18, column: 9, scope: !3)
!17 = !DILocalVariable(name: "x", scope: !3, file: !0, line: 19, type: !5)
!18 = !DILocation(line: 19, column: 9, scope: !3)
!19 = !DILocalVariable(name: "y", scope: !3, file: !0, line: 20, type: !5)
!20 = !DILocation(line: 20, column: 9, scope: !3)
!21 = !DILocation(line: 17, column: 25, scope: !3)
!22 = !DILocation(line: 17, column: 32, scope: !3)
!23 = !DILocation(line: 18, column: 31, scope: !3)
!24 = !DILocation(line: 18, column: 47, scope: !3)
!25 = !DILocation(line: 18, column: 54, scope: !3)
!26 = !DILocation(line: 19, column: 13, scope: !3)
!27 = !DILocation(line: 19, column: 5, scope: !3)
!28 = !DILocation(line: 20, column: 25, scope: !3)
!29 = !DILocation(line: 20, column: 32, scope: !3)
!30 = !DILocation(line: 20, column: 13, scope: !3)
!31 = !DILocation(line: 20, column: 5, scope: !3)
!32 = !DILocation(line: 21, column: 1, scope: !3)
!33 = !{i32 7, !"Dwarf Version", i32 4}
!34 = !{i32 2, !"Debug Info Version", i32 3}
