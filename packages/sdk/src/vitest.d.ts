declare module "vitest" {
  type ExpectFn = ((...args: any[]) => any) & {
    unreachable: (...args: any[]) => never;
  };

  export const describe: (...args: any[]) => any;
  export const it: (...args: any[]) => any;
  export const expect: ExpectFn;
  export const beforeEach: (...args: any[]) => any;
  export const vi: {
    fn: (...args: any[]) => any;
  };
}
