import { ExampleProvider } from "../src/index.js";

async function main(): Promise<void> {
  const provider = new ExampleProvider();
  const healthy = await provider.healthCheck("conn_demo");
  const response = await provider.proxy({
    method: "GET",
    path: "/tickets"
  });

  console.log(
    JSON.stringify(
      {
        healthy,
        response
      },
      null,
      2
    )
  );
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
