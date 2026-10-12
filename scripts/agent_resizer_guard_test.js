// Agent 任务区 resizer 拖拽守卫测试（Node 静态断言）
// 防止回归：①max 固定 320 导致"只能拖到一半"；②下方空白无法利用；③窗口 resize 不夹紧
const fs = require("fs");
const path = require("path");
const src = fs.readFileSync(path.join(__dirname, "..", "cmd", "contextloom-gui", "web", "app.js"), "utf8");
let fails = 0;
const check = (name, cond, detail) => {
  if (cond) console.log("PASS  " + name);
  else { fails++; console.log("FAIL  " + name + (detail ? "  <- " + detail : "")); }
};

// 1. 动态上限函数必须存在
check("agentTasksMax 定义存在", /function\s+agentTasksMax\s*\(/.test(src));
// 2. initAgentResizer 的 max 必须引用动态函数（而非固定常量 AGENT_H_MAX）
{
  const m = src.match(/function\s+initAgentResizer[\s\S]*?\n\}/);
  const body = m ? m[0] : "";
  check("initAgentResizer 内 max 引用 agentTasksMax", /max:\s*agentTasksMax/.test(body),
    "固定 AGENT_H_MAX 会导致任务区只能拖到 320px");
}
// 3. saved 恢复必须走 applyAgentH（动态夹紧），不得再用 saved <= AGENT_H_MAX 拒绝
{
  const m = src.match(/function\s+initAgentResizer[\s\S]*?\n\}/);
  const body = m ? m[0] : "";
  check("saved 恢复走 applyAgentH", /applyAgentH\(saved\)/.test(body));
  check("saved 恢复无 AGENT_H_MAX 上限拒绝", !/saved\s*&&\s*saved\s*>=\s*AGENT_H_MIN\s*&&\s*saved\s*<=\s*AGENT_H_MAX/.test(body));
}
// 4. applyAgentH 存在且夹紧到动态 max
check("applyAgentH 定义存在", /function\s+applyAgentH\s*\(/.test(src));
{
  const m = src.match(/function\s+applyAgentH[\s\S]*?\n\}/);
  const body = m ? m[0] : "";
  check("applyAgentH 内部夹紧到 agentTasksMax()", /agentTasksMax\(\)/.test(body));
}
// 5. 窗口 resize 后重新夹紧（防溢出）
{
  const m = src.match(/function\s+initAgentResizer[\s\S]*?\n\}/);
  const body = m ? m[0] : "";
  check("resize listener 内重新 applyAgentH", /addEventListener\("resize"[\s\S]*?applyAgentH\(parseFloat\(tasks\.style\.height\)\)/.test(body));
}
// 6. agentTasksMax 计算保留 goalPanel 最小高度
{
  const m = src.match(/function\s+agentTasksMax[\s\S]*?\n\}/);
  const body = m ? m[0] : "";
  check("agentTasksMax 扣除 AGENT_GOAL_MIN", /AGENT_GOAL_MIN/.test(body));
}
// 7. CSS 命中区加大
{
  const css = fs.readFileSync(path.join(__dirname, "..", "cmd", "contextloom-gui", "web", "style.css"), "utf8");
  check("style.css #agentResizer flex-basis 8px", /#agentResizer\s*\{\s*flex-basis:\s*8px/.test(css));
}
// 8. 语法校验（权威：new Function 编译整个脚本，语法错误即抛异常）
{
  try {
    new Function(src);
    check("app.js 语法合法 (new Function)", true);
  } catch (e) {
    check("app.js 语法合法 (new Function)", false, e.message);
  }
}
console.log(fails === 0 ? "\n全部通过" : "\n" + fails + " 项失败");
process.exit(fails === 0 ? 0 : 1);
