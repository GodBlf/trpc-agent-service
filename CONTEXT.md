# Agent Service Context

本项目提供基于 tRPC-Agent-Go 的多租户、节点化 Agent 平台。本文统一平台领域中的核心名词，避免平台对象与上游 Agent 运行时能力混用。

## Tenant And Application

**Tenant**:
平台中配置、权限、运行数据和审计边界的租户。
_Avoid_: Account, Customer

**Agent App**:
租户注册、配置、发布并可被请求路由到的 Agent 应用。
_Avoid_: Agent, Bot, Worker

**Deployment**:
Agent App 的一条独立发布记录，关联选定的 Deployment Version 及其生命周期状态。
_Avoid_: Instance, Node

**Active Deployment**:
同一 Tenant 和 Agent App 范围内唯一可接收新路由请求的 Deployment。
_Avoid_: Primary Deployment, Default Deployment

## Runtime And Routing

**Gateway**:
接收外部请求，并负责租户、Agent App、Channel Binding 和 Session 路由的平台入口。
_Avoid_: Runner

**Worker**:
执行 Agent App 的运行进程或实例。
_Avoid_: Agent App, Node

**Node**:
承载 Gateway、Worker 或 Channel Adapter 等运行单元的可调度平台运行单元。
_Avoid_: Worker, Host

## Conversation And Data

**Channel Binding**:
租户和 Agent App 与某个外部 IM 账号、回调入口或通道配置之间的绑定关系。
_Avoid_: Channel, IM Account

**Session**:
某个租户用户在特定会话范围内形成的连续交互上下文。
_Avoid_: Conversation, Chat

**Session Event**:
按会话顺序记录的消息、状态变化或 Agent 执行事件。
_Avoid_: Log, Message

**Memory**:
可跨 Session 检索的长期信息。
_Avoid_: Session History, Context

**Summary**:
对 Session 历史进行压缩后的上下文表示。
_Avoid_: Memory, Snapshot

**Failure Event**:
描述 Agent 执行失败的 Session Event，例如 `run.failed`，必须可被管理界面检查。
_Avoid_: Application Error, Audit Event

**Migration Job**:
在源 Storage Adapter 与目标 Storage Adapter 之间复制 Session Event 和 Memory，并记录进度与校验结果的受控操作。
_Avoid_: Data Copy, Offline Script

**Migration Checkpoint**:
标记 Migration Job 已完成位置、用于中断后恢复的持久化进度。
_Avoid_: In-Memory Progress, Cursor

**Audit Event**:
用于安全、合规和运营追踪的审计记录。
_Avoid_: Application Log, Trace

## Delivery And Integration

**Phase**:
一个可独立开发、测试并移交的纵向能力切片，具有明确范围、验收门槛和下一阶段输入。
_Avoid_: Milestone, Sprint

**Tenant Context**:
由受信任入口解析并注入请求的租户身份上下文，业务服务据此执行资源隔离。
_Avoid_: tenant_id request field, User Context

**Runner Adapter**:
平台 Worker 调用 Agent runtime 的稳定端口及其具体实现之间的适配边界。
_Avoid_: Worker, Agent App

**Framework Runtime**:
由 `trpc-agent-go` 提供的 Agent 执行能力，包括 Agent、Runner、模型消息和运行事件；平台通过 Runner Adapter 使用它，不直接把其内部类型暴露给外部接口。
_Avoid_: Worker, Platform Runtime

**AgentFactory**:
根据已发布 Deployment Version 构建租户 Agent 运行实例的稳定平台能力；它决定 Agent 配置如何进入 Framework Runtime。
_Avoid_: Runner, Agent Registry

**Channel Adapter**:
将外部 IM 入站/出站消息转换为平台消息和 Runner Event 的通道集成边界。
_Avoid_: Channel Binding, Webhook Handler

**Storage Adapter**:
对 Session、Memory、Summary、Artifact、Knowledge 和 Audit Event 等平台数据提供统一访问契约的后端适配边界。
_Avoid_: Database Driver, Repository (as a backend choice)

**Backend Selection**:
Tenant 选择的、由服务端配置并授权的 Storage Adapter，用于该 Tenant 的数据访问。
_Avoid_: Client-Selected Backend, Database Preference

**Public Error Contract**:
面向 API 消费者的稳定错误码与脱敏消息，不包含后端地址、路径、凭据或驱动细节。
_Avoid_: Raw Driver Error, Internal Diagnostic

**Deployment Version**:
Agent App 一次可发布、可路由并可回滚的配置版本。
_Avoid_: Node, Worker Instance

**Mock IM**:
实现标准 Channel Adapter 契约并可注入重复、乱序、超时、限流和重试故障的测试通道。
_Avoid_: Fake Channel (when it omits failure semantics)

**Management Console**:
平台管理员、租户管理员和运维人员管理平台资源、策略与运行状态的渐进式 Web 应用。
_Avoid_: Admin API, Chat Client

**Chat Workspace**:
Management Console 中通过 Web UI 创建或恢复 Session、发送消息并查看流式 Agent 回复的交互区域，也是 Mock IM 的本地可视化验证入口。
_Avoid_: Real IM, Channel Adapter

**Development Identity**:
仅用于开发和自动化验收、由服务端校验并建立 Tenant Context 的非生产身份。
_Avoid_: tenant_id request field, Production Identity

**Real IM Provider**:
通过真实平台协议实现 Channel Adapter 的外部消息提供方；本项目要求的两个实现为 Enterprise WeChat 和 Telegram。
_Avoid_: Mock IM, Chat Workspace
