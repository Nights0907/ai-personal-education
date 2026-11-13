---
name: weakness-diagnosis
description: 在 DIAGNOSE 模式下，从题目关联的学生错解或引导作答提取可回溯的薄弱知识点及能力证据；没有证据时提交空结果。
---

调用 get_task_input、get_question_context 和 get_knowledge_catalog。只分析服务端指定的 source_record_id 集合，不能从其他学生或其他任务推断。

## 证据判定

每条证据至少包含：原作答片段与准确引用、具体数学错误、对应知识点或能力编码、影响程度。引用以工具提供的规范化文本为准，提供 source_record_id、原文 quote 和从 1 开始的 occurrence_index；字节偏移由服务端精确匹配计算，并回溯原图块或原引导消息。不能正确定位片段就不输出该证据。

支持 item_type 为 knowledge 或 capability；同一错误支持两种条目时分别说明关联原因。impact 为 local 或 blocks_key_step。只使用工具返回的标签，不能自行新建知识点或能力。

以下情况不新增确定薄弱证据：只上传题目、仅点击某步骤、只有自述不会、教师示范、学生提示后答对。真实引导中的错误可以说明本次困难，但必须标记 tutoring 来源。

避免把偶发错误上升为稳定能力缺陷；不用“粗心”替代具体数学错误，不作智力或人格归因。正确的非标准解法不能因不同于参考步骤而标为错误。OCR 无法可靠支持的错解，不以猜测构造证据。

候选为空是有效结果。建议关注（一般/中等/重要）、聚合计数和最近出现时间由业务层计算，不能作为候选证据的可修改参数。

按 [工具契约](../../contracts/tools.json) 调用 validate_diagnosis；passed=true 后调用 finalize_diagnosis，提交不可变 draft_id。本次运行至此结束。
