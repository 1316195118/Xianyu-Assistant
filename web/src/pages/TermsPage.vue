<script setup>
import PublicLayout from './PublicLayout.vue'

const origin = location.origin
</script>

<template>
  <PublicLayout
    eyebrow="使用规则 · 清楚边界"
    title="服务条款"
    intro="鱼助手帮助你整理自己的闲鱼买卖、成本、寄卖和结算数据。使用前请理解：它是自托管记账软件，不是闲鱼官方服务，也不替部署者作出交易、税务或法律决定。"
  >
    <template #hero>
      <div class="public-meta-line">
        <span>生效日期：2026 年 11 月 23 日</span>
        <span>适用服务：{{ origin }}</span>
      </div>
    </template>

    <article class="public-card">
      <nav class="public-toc" aria-label="服务条款目录">
        <a href="#acceptance">接受条款</a>
        <a href="#service">服务说明</a>
        <a href="#accounts">账户安全</a>
        <a href="#content">数据与内容</a>
        <a href="#acceptable-use">合理使用</a>
        <a href="#ip">知识产权</a>
        <a href="#availability">可用性与备份</a>
        <a href="#disclaimer">免责声明</a>
        <a href="#disputes">争议处理</a>
      </nav>

      <section class="legal-section" id="acceptance">
        <h2>1. 接受条款</h2>
        <p>访问或使用鱼助手，即表示你已阅读并同意本条款和隐私政策。如果你不同意，请停止使用。若你代表店铺、团队或其他组织使用，应确认你有权作出该同意。</p>
      </section>

      <section class="legal-section" id="service">
        <h2>2. 自托管软件与部署运营者</h2>
        <p>鱼助手是开源自托管软件，提供交易记录、订单同步、成本分摊、利润统计、寄卖代采、备份和数据导出等功能。具体版本、功能开关和可用性由当前部署决定。</p>
        <p>实际域名、服务器、数据库、备份、账号管理和客户支持由部署运营者负责。项目维护者不当然运营你正在访问的实例，也不能直接访问该实例中的经营数据。</p>
      </section>

      <section class="legal-section" id="accounts">
        <h2>3. 账户、密码与访问安全</h2>
        <ul>
          <li>你应设置足够强的管理密码、店铺密码或只读密码，并避免多人共用同一高强度管理账号。</li>
          <li>登录 Cookie、闲鱼 Cookie 和 OCR API Key 都属于敏感凭证，不得分享给无关人员或提交到公开代码仓库。</li>
          <li>你应自行判断只读账号、店铺隔离和网络访问范围是否满足团队权限要求。</li>
          <li>发现凭证泄露、异常登录或数据被误改时，应立即退出登录、更换密码、重新绑定闲鱼登录态并检查备份。</li>
        </ul>
      </section>

      <section class="legal-section" id="content">
        <h2>4. 数据、内容与责任</h2>
        <p>你保留自己录入的交易、寄卖、结算、截图和备注等数据的权利，并对其合法性、准确性和使用权限负责。你不得上传无权处理的个人信息、商业秘密或侵犯他人权益的内容。</p>
        <p>利润、成本、运费、平台费用和结算结果取决于你录入的数据、配置和平台返回信息。鱼助手按现有规则计算，但不保证第三方平台数据始终完整、实时或准确。重要财务决定前应结合平台账单、物流记录和实际收款人工复核。</p>
      </section>

      <section class="legal-section" id="acceptable-use">
        <h2>5. 合理使用</h2>
        <p>你不得利用鱼助手从事违法活动、绕过平台安全机制、批量盗取他人数据、发送垃圾信息、传播恶意代码、攻击服务，或干扰其他用户和第三方平台。访问闲鱼等第三方服务时，还应遵守其自身规则和接口限制。</p>
      </section>

      <section class="legal-section" id="ip">
        <h2>6. 知识产权</h2>
        <p>鱼助手的原创软件、界面和文档受适用法律及项目许可证保护。开源许可证赋予你的权利以仓库中的许可证文本为准。</p>
        <p>“闲鱼”“淘宝”“天猫”等名称和商标归其权利人所有。鱼助手仅以描述兼容场景的目的使用相关名称，不表示获得官方授权、合作或背书。你上传的订单截图、数据和第三方内容仍归原权利人。</p>
      </section>

      <section class="legal-section" id="third-parties">
        <h2>7. 第三方服务</h2>
        <p>闲鱼同步、视觉 OCR、GitHub 更新、代理、反向代理和托管存储等功能可能依赖第三方。第三方服务的中断、规则变化、费用、数据处理或内容限制由相应服务商决定。启用前请自行确认其条款和风险。</p>
      </section>

      <section class="legal-section" id="availability">
        <h2>8. 可用性、维护与备份</h2>
        <p>部署运营者可能因升级、维护、网络故障、服务器资源不足或安全事件暂停服务。鱼助手提供手动和定时备份能力，但备份成功率和可恢复性仍取决于部署者的磁盘、目录权限、异地副本和验证流程。</p>
        <p>你应定期下载必要数据，并确认备份能够恢复。不要只依赖一块磁盘、一个服务器或一种自动备份方式。</p>
      </section>

      <section class="legal-section" id="disclaimer">
        <h2>9. 免责声明与责任限制</h2>
        <p>在适用法律允许的范围内，鱼助手按“现状”提供。因数据录入错误、第三方接口变更、闲鱼登录失效、网络中断、设备故障、配置不当、未备份、误删除或不可抗力造成的损失，由相应使用者和部署运营者按实际情况承担。</p>
        <p>本条款不排除或限制法律不得排除或限制的责任。涉及税务、会计、消费者权益、个人信息保护或商业合同的事项，请咨询具备相应资质的专业人士。</p>
      </section>

      <section class="legal-section" id="termination">
        <h2>10. 暂停与终止</h2>
        <p>部署运营者可因安全风险、违法使用、资源滥用或维护需要限制访问。你可随时停止使用、导出数据和删除部署。终止后，数据能否恢复取决于部署者是否保留数据库或备份。</p>
      </section>

      <section class="legal-section" id="changes">
        <h2>11. 条款更新</h2>
        <p>功能、法律要求或第三方服务发生变化时，条款可能更新并在本页标注生效日期。继续使用更新后的服务，视为接受修订后的条款；如不同意，应停止使用。</p>
      </section>

      <section class="legal-section" id="disputes">
        <h2>12. 适用规则与争议处理</h2>
        <p>争议应首先基于项目文档、操作记录、平台账单和双方约定协商解决。具体适用法律、管辖法院、消费者保护或商业争议规则，应由实际部署运营者根据其所在地和业务性质补充。</p>
        <p>本页面未填写虚构的运营者名称或联系方式。请通过提供当前部署地址或运维支持的渠道联系负责人；开源代码问题可通过项目仓库的公开 Issue 渠道反馈。</p>
        <div class="legal-callout">这是一份通用产品条款初稿，不构成针对特定主体、地区或行业的法律意见。正式商业运营前，建议由当地专业人士审阅。</div>
      </section>
    </article>

    <div class="public-actions">
      <a class="public-action primary" href="/">进入经营账本</a>
      <a class="public-action" href="/privacy">查看隐私政策</a>
    </div>
  </PublicLayout>
</template>
