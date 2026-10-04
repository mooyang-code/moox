# 《申论训练》OBE 教案生成实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 依据已确认的课程大纲和设计规范，生成一份包含 27 个教学单元、80 学时且可直接归档使用的 OBE Word 教案。

**Architecture:** 在 `/tmp/essay-training-lesson-plan` 建立隔离的 Python 构建目录，将课程数据、27 个单元内容、附录量规、Word 渲染器和校验器分离。使用 `python-docx` 生成成品，用结构化数据校验学时、目标映射和教学流程，再重新打开 Word 文件检查包结构与正文内容。临时构建文件不进入项目仓库，只提交设计、计划和最终 Word 文档。

**Tech Stack:** Python 3、python-docx、Python 标准库、Microsoft Word OOXML

---

## 文件结构

- 创建：`/tmp/essay-training-lesson-plan/model.py`，定义课程、单元、学习成果、教学环节和量规的数据模型。
- 创建：`/tmp/essay-training-lesson-plan/course.py`，保存课程基本信息、课程目标、毕业要求映射和总体评价方案。
- 创建：`/tmp/essay-training-lesson-plan/units_01_07.py`，保存申论认知、真题分析和作答流程单元。
- 创建：`/tmp/essay-training-lesson-plan/units_08_15.py`，保存归纳概括题和综合分析题单元。
- 创建：`/tmp/essay-training-lesson-plan/units_16_23.py`，保存提出对策题和贯彻执行题单元。
- 创建：`/tmp/essay-training-lesson-plan/units_24_27.py`，保存申发论述题单元。
- 创建：`/tmp/essay-training-lesson-plan/appendices.py`，保存评价量规、反思汇总表和参考资料。
- 创建：`/tmp/essay-training-lesson-plan/render_docx.py`，负责 Word 版式、目录、表格、页眉和页码。
- 创建：`/tmp/essay-training-lesson-plan/validate.py`，负责内容与成品校验。
- 创建：`docs/《申论训练》课程教案（OBE版）.docx`，项目内交付文件。
- 创建：源大纲文件夹下的 `《申论训练》课程教案（OBE版）.docx`，外部交付副本。

### Task 1：建立隔离构建环境

**Files:**
- Create: `/tmp/essay-training-lesson-plan/.venv`

- [ ] **Step 1：确认临时目录并创建构建目录**

Run:

```bash
ls -ld "/tmp"
mkdir -p "/tmp/essay-training-lesson-plan"
```

Expected: `/tmp` 存在，构建目录创建成功。

- [ ] **Step 2：创建虚拟环境并安装最新 python-docx**

Run:

```bash
python3 -m venv "/tmp/essay-training-lesson-plan/.venv"
"/tmp/essay-training-lesson-plan/.venv/bin/python" -m pip install --upgrade pip python-docx
```

Expected: 安装成功，无依赖冲突。

- [ ] **Step 3：验证依赖**

Run:

```bash
"/tmp/essay-training-lesson-plan/.venv/bin/python" -c "import docx; print(docx.__version__)"
```

Expected: 输出已安装的 `python-docx` 版本。

### Task 2：建立素材来源清单

**Files:**
- Create: `/tmp/essay-training-lesson-plan/sources.md`

- [ ] **Step 1：核对官方考试与政策来源**

检索国家公务员局考试录用公务员专题网站、中国政府网及教育部公开信息，记录课程可引用的考纲、政府工作报告、乡村振兴、基层减负、数字治理、公共文化服务和行政执法规范化材料。每条记录包含标题、发布机构、发布日期和 URL。

- [ ] **Step 2：核对近年国考真题主题**

选取近五年省级、市（地）级和行政执法类申论公开题目摘要，交叉核对年份、试卷类别和主题。只记录教学所需的主题、任务类型和能力要求，不复制整段材料或完整题目。

- [ ] **Step 3：写入来源清单**

`sources.md` 至少包含：

1. 国家公务员公共科目笔试考试大纲；
2. 源大纲列出的两本建议教材；
3. 至少四项官方公共治理政策材料；
4. 至少六套近五年国考申论试卷的主题记录。

Expected: 所有案例均可追溯，不使用无法核验的年份、题名或政策表述。

### Task 3：先建立数据模型和失败校验

**Files:**
- Create: `/tmp/essay-training-lesson-plan/model.py`
- Create: `/tmp/essay-training-lesson-plan/validate.py`

- [ ] **Step 1：定义数据模型**

实现以下不可变数据结构：

```python
from dataclasses import dataclass


@dataclass(frozen=True)
class Outcome:
    text: str
    course_goal: str
    evidence: str


@dataclass(frozen=True)
class Stage:
    name: str
    minutes: int
    teacher_activity: str
    student_activity: str
    purpose: str
    evidence: str
    course_goal: str


@dataclass(frozen=True)
class Unit:
    number: int
    chapter: str
    title: str
    lecture_hours: int
    practice_hours: int
    lesson_type: str
    learner_analysis: str
    outcomes: tuple[Outcome, ...]
    key_points: tuple[str, ...]
    difficult_points: tuple[str, ...]
    strategies: tuple[str, ...]
    methods: tuple[str, ...]
    resources: tuple[str, ...]
    ideological_goal: str
    ideological_case: str
    stages: tuple[Stage, ...]
    board_plan: tuple[str, ...]
    assessment: tuple[str, ...]
    basic_assignment: str
    advanced_assignment: str
    reflection_dimensions: tuple[str, ...]
```

- [ ] **Step 2：编写内容校验函数**

`validate.py` 必须检查：

```python
def validate_units(units):
    assert [unit.number for unit in units] == list(range(1, 28))
    assert sum(unit.lecture_hours for unit in units) == 32
    assert sum(unit.practice_hours for unit in units) == 48
    assert sum(unit.lecture_hours + unit.practice_hours for unit in units) == 80
    assert sum(sum(stage.minutes for stage in unit.stages) for unit in units) == 80 * 45

    for unit in units:
        expected_minutes = (unit.lecture_hours + unit.practice_hours) * 45
        assert sum(stage.minutes for stage in unit.stages) == expected_minutes
        assert len(unit.outcomes) >= 3
        assert unit.key_points and unit.difficult_points and unit.strategies
        assert unit.ideological_goal and unit.ideological_case
        assert unit.assessment
        assert unit.basic_assignment and unit.advanced_assignment
        assert all(outcome.course_goal in {"课程目标1", "课程目标2", "课程目标3"} for outcome in unit.outcomes)
        assert all(outcome.evidence for outcome in unit.outcomes)
        assert all(stage.evidence and stage.course_goal for stage in unit.stages)
```

- [ ] **Step 3：运行校验并确认失败**

Run:

```bash
cd "/tmp/essay-training-lesson-plan"
".venv/bin/python" validate.py
```

Expected: 因课程内容模块尚未创建而失败，证明校验入口有效。

### Task 4：编写课程总说明与第 1—7 单元

**Files:**
- Create: `/tmp/essay-training-lesson-plan/course.py`
- Create: `/tmp/essay-training-lesson-plan/units_01_07.py`

- [ ] **Step 1：编写课程总说明**

准确转写源大纲中的课程信息、三项课程目标、毕业要求支撑、考核构成和课程思政主线。将过程考核 30% 与期末考核 70% 映射到课程目标，不复制源大纲中明显重复或编号错误的达成度公式。

- [ ] **Step 2：编写单元 1—3**

覆盖申论认知、考纲与评分标准、三类国考真题比较。单元 3 采用 5 讲授学时加 3 实践学时，教学过程依次包含类型比较、命题意图识别、典型题拆解和限时迁移训练。

- [ ] **Step 3：编写单元 4—7**

覆盖作答流程总览、审题、提炼要点、加工与规范书写。每单元 2 讲授学时，形成“审题条件清单—材料标注—要点归类—规范表达”的连续成果链。

- [ ] **Step 4：运行局部校验**

检查 1—7 单元编号连续、合计 20 学时，讲授 17 学时、实践 3 学时，所有教学流程时间等于对应学时。

### Task 5：编写第 8—15 单元

**Files:**
- Create: `/tmp/essay-training-lesson-plan/units_08_15.py`

- [ ] **Step 1：编写归纳概括题四单元**

单元 8—11 依次为考题认知、技法强化、真题精解、前瞻预测与迁移训练。训练“主体分析法、关键词聚类、同义转述、抽象升华”，使用公共服务、基层治理等案例。

- [ ] **Step 2：编写综合分析题四单元**

单元 12—15 依次为考题认知、技法强化、真题精解、前瞻预测与迁移训练。训练“本质提炼、关系辨析、价值判断”，覆盖解释型、评论型和启示型任务。

- [ ] **Step 3：运行局部校验**

检查 8—15 单元合计 24 学时，其中讲授 6 学时、实践 18 学时；每单元均包含个人作答、同伴互评和二次修改证据。

### Task 6：编写第 16—23 单元

**Files:**
- Create: `/tmp/essay-training-lesson-plan/units_16_23.py`

- [ ] **Step 1：编写提出对策题四单元**

单元 16—19 训练“问题归因、对策适配、可行性验证”，突出针对性、操作性、协同性和风险预判。案例覆盖老旧小区改造、乡村振兴与基层减负。

- [ ] **Step 2：编写贯彻执行题四单元**

单元 20—23 训练文种识别、对象意识、结构搭建和语言适配，覆盖提纲、讲话稿、倡议书、工作建议等常见任务，不机械套用完整法定公文格式。

- [ ] **Step 3：运行局部校验**

检查 16—23 单元合计 24 学时，其中讲授 6 学时、实践 18 学时；评价证据至少包含一份对策清单和一份完整应用文作答。

### Task 7：编写第 24—27 单元与附录

**Files:**
- Create: `/tmp/essay-training-lesson-plan/units_24_27.py`
- Create: `/tmp/essay-training-lesson-plan/appendices.py`

- [ ] **Step 1：编写申发论述题四单元**

单元 24—27 训练审题立意、论证结构、论据运用、语言表达和综合模拟。每单元包含提纲产出，至少两个单元包含完整限时作文与分项反馈。

- [ ] **Step 2：编写评价量规**

为课堂表现、讨论、作业和实训分别设计五级量规。每级使用可观察描述，评价维度覆盖材料依据、要点完整性、逻辑结构、政策表达、格式规范和时间控制。

- [ ] **Step 3：编写教学反思汇总表与参考资料**

反思表保留“目标达成、时间控制、学生困难、反馈效果、改进措施”五个记录维度，不预写虚构结论。参考资料合并源大纲教材与 Task 2 核验过的官方来源。

- [ ] **Step 4：运行全量内容校验**

Run:

```bash
cd "/tmp/essay-training-lesson-plan"
".venv/bin/python" validate.py
```

Expected: 27 个单元、80 学时、32 讲授学时、48 实践学时及 3600 分钟全部通过。

### Task 8：实现 Word 渲染器

**Files:**
- Create: `/tmp/essay-training-lesson-plan/render_docx.py`

- [ ] **Step 1：实现页面与字体样式**

设置 A4、上下边距 2.54 厘米、左右边距 2.8 厘米。封面标题使用黑体，一级标题使用黑体，正文使用宋体，西文字体使用 Times New Roman。正文采用 1.5 倍行距，表格采用五号字和单倍行距。

- [ ] **Step 2：实现目录、页眉与页码**

插入 Word TOC 域，设置打开文档时更新域。正文页眉显示“《申论训练》课程教案（OBE版）”，页脚居中插入 `PAGE` 域。

- [ ] **Step 3：实现总说明、单元和附录渲染**

每个单元从新页开始。教学过程表包含“阶段与时间、教师活动、学生活动、设计意图、成果证据、目标映射”六列；跨页时重复表头。教学反思只保留可填写行。

- [ ] **Step 4：生成项目内 Word 文件**

Run:

```bash
cd "/tmp/essay-training-lesson-plan"
".venv/bin/python" render_docx.py "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/《申论训练》课程教案（OBE版）.docx"
```

Expected: 生成非空 `.docx` 文件，程序退出码为 0。

### Task 9：校验 Word 成品并复制交付

**Files:**
- Modify: `/tmp/essay-training-lesson-plan/validate.py`
- Create: `/Users/mooyang/Library/Containers/com.tencent.xinWeChat/Data/Documents/xwechat_files/wxid_afwgc2j4eemv12_3927/msg/file/2026-08/《申论训练》课程教案（OBE版）.docx`

- [ ] **Step 1：增加成品校验**

使用 `python-docx` 重新打开成品，检查封面课程名、27 个单元标题、第五章综合分析题、第六章提出对策题、附录标题、页眉、页脚和表格数量。遍历段落及表格单元格，确认不存在设计规范禁止的占位标记。

- [ ] **Step 2：检查 OOXML 包**

Run:

```bash
python3 -m zipfile -t "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/《申论训练》课程教案（OBE版）.docx"
```

Expected: 输出 `Done testing`。

- [ ] **Step 3：运行完整校验**

Run:

```bash
cd "/tmp/essay-training-lesson-plan"
".venv/bin/python" validate.py "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/《申论训练》课程教案（OBE版）.docx"
```

Expected: 输出 27 个单元、80/32/48 学时、3600 分钟、占位扫描和 Word 重开检查全部通过。

- [ ] **Step 4：复制到源文件夹并核对哈希**

Run:

```bash
cp "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/《申论训练》课程教案（OBE版）.docx" "/Users/mooyang/Library/Containers/com.tencent.xinWeChat/Data/Documents/xwechat_files/wxid_afwgc2j4eemv12_3927/msg/file/2026-08/《申论训练》课程教案（OBE版）.docx"
shasum -a 256 "/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/docs/《申论训练》课程教案（OBE版）.docx" "/Users/mooyang/Library/Containers/com.tencent.xinWeChat/Data/Documents/xwechat_files/wxid_afwgc2j4eemv12_3927/msg/file/2026-08/《申论训练》课程教案（OBE版）.docx"
```

Expected: 两个文件的 SHA-256 完全一致。

### Task 10：最终审阅、提交并同步

**Files:**
- Verify: `docs/《申论训练》课程教案（OBE版）.docx`
- Verify: `docs/superpowers/specs/2026-08-26-essay-training-obe-lesson-plan-design.md`
- Verify: `docs/superpowers/plans/2026-08-26-essay-training-obe-lesson-plan.md`

- [ ] **Step 1：执行最终检查**

重新核对设计规范的九项校验标准，确认原工作区中与本任务无关的既有修改未被改动或暂存。

- [ ] **Step 2：检查 Git 变更**

Run:

```bash
git status --short
git diff --check
git diff --stat
```

Expected: 本任务仅新增计划和最终 Word 文件；其他既有修改保持未暂存状态。

- [ ] **Step 3：提交本任务文件**

Run:

```bash
git add "docs/superpowers/plans/2026-08-26-essay-training-obe-lesson-plan.md" "docs/《申论训练》课程教案（OBE版）.docx"
git commit -m "docs(course): add OBE lesson plan for essay training"
```

Expected: 提交成功，未包含用户原有的其他修改。

- [ ] **Step 4：推送当前分支**

Run:

```bash
git push
```

Expected: 当前分支提交同步到远程仓库。
